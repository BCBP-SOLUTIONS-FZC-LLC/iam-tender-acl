package http

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	gincommon "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-gincommon/pkg/gincommon"
)

// requiredRoles are the RequestContext roles allowed to call TAC-1/2/3
// (LLD §8.2): resolved entirely from the gateway-injected x-tenant-roles
// header, no DB round trip.
var requiredRoles = []string{"tender_admin", "tenant_admin", "tenant_owner"}

// DocsConfig controls whether — and how — the Swagger UI docs surface is
// exposed. Outside production it's always on; in production it's opt-in
// via Enabled and, if AuthToken is set, gated behind a bearer token so the
// API surface isn't exposed to the open internet by default. Mirrors
// iam-org-membership's identical DocsConfig.
type DocsConfig struct {
	Environment string
	Enabled     bool
	AuthToken   string
}

func (d DocsConfig) active() bool {
	return d.Environment != "production" || d.Enabled
}

// Router builds and owns the Gin engine for this service.
type Router struct {
	engine *gin.Engine
}

// NewRouter wires every route: the public admin API (TAC-1/2/3, role-gated
// via RequestContext.Roles), the internal mTLS-only access-check API
// (TAC-4, no RBAC — tenant isolation from RLS alone), and the health
// checks.
func NewRouter(h *Handler, postgres PostgresHealth, cache Pinger, ginCfg gincommon.Config, docs DocsConfig) *Router {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()

	// ServiceName is required by ObservabilityMiddlewares (empty panics —
	// invalid Prometheus labels). Tests that never go through main.go
	// still need a name; production always passes ginCfg from the
	// composition root, already primed there so metrics.Init is a
	// sync.Once no-op here.
	if ginCfg.ServiceName == "" {
		ginCfg.ServiceName = "tender-acl"
	}
	// 30s hard deadline on every request — gincommon's TimeoutMiddleware,
	// matching iam-org-membership / iam-realm-provisioner. Must run before
	// ObservabilityMiddlewares so a timeout is recorded on
	// http_request_timeout_total rather than hanging until the handler
	// returns.
	engine.Use(gincommon.TimeoutMiddleware(30 * time.Second))
	// Observability (recovery/request-id/tracing/metrics/correlation/logging)
	// applies to every route, including TAC-4. Auth (ProtectedMiddlewares)
	// applies only to the public admin group below — TAC-4 is a mesh-only
	// trust boundary with no RBAC/JWT check (LLD §8.2/§13.2).
	//
	// Tracing comes entirely from gincommon: cmd/tender-acl/main.go calls
	// gincommon.InitTracing at process start so in-process spans get valid
	// trace IDs even before this engine is built, then ObservabilityMiddlewares
	// attaches TracingMiddleware. EnsureInitTelemetry is a no-op once the
	// SDK provider is already installed. There is no separate otelgin
	// middleware or hand-rolled TracerProvider in this process.
	//
	// Generic per-request HTTP metrics (count/duration/status, by
	// method+route) are gincommon's own http_requests_total/
	// http_request_duration_seconds (ObservabilityMiddlewares'
	// MetricsMiddleware) — passed through as-is. internal/adapter/outbound/metrics
	// still owns every metric gincommon has no equivalent for — writes,
	// grant checks, cache hits/misses, and both cascades.
	engine.Use(gincommon.ObservabilityMiddlewares(ginCfg)...)

	public := engine.Group("/api/v1/tenants/:id/tenders/:tender_id/acl")
	public.Use(gincommon.ProtectedMiddlewares(ginCfg)...)
	public.Use(ContextBridgeMiddleware())
	public.GET("", h.List)
	public.POST("", h.Grant)
	public.DELETE("/:user_id", h.Revoke)

	// TAC-4/I-12: mesh-only, mTLS trust boundary — no RBAC. Registered
	// without an /api/v1 prefix, matching LLD §8.3's table and
	// iam-org-membership's actual registration; §8.4's header text showing
	// /api/v1/internal/... was the LLD's own internal inconsistency,
	// since corrected (LLD §8.4).
	internalGroup := engine.Group("/internal/tenants/:id/tenders/:tender_id/acl")
	internalGroup.GET("/:user_id", h.CheckAccess)

	hc := &healthHandlers{postgres: postgres, cache: cache}
	// healthz is gincommon.HealthHandler() itself, not a local
	// reimplementation — a shared-library reuse audit found this route
	// used to reproduce that handler's exact {"status":"ok"} body by hand.
	engine.GET("/healthz", gincommon.HealthHandler())
	engine.GET("/readyz", hc.readyz)

	registerDocsRoutes(engine, docs)

	return &Router{engine: engine}
}

// registerDocsRoutes wires the Swagger UI (REST) and AsyncAPI viewer
// (events) doc surfaces. Swagger renders the OpenAPI spec generated from
// the // @… annotations by `make swag` (docs/swagger/docs.go's init()
// registers it; see cmd/tender-acl/swagger_info.go for the top-level spec
// metadata). The AsyncAPI viewer (asyncapi.go) renders api/asyncapi.yaml —
// embedded at compile time (api/embed.go), so it needs no file present at
// runtime — as a browsable HTML catalog at GET /asyncapi, plus the raw
// spec verbatim at GET /asyncapi.yaml, ported from iam-user-profile's
// identical viewer. Outside production both are always mounted; in
// production they're opt-in via DocsConfig.Enabled and, if AuthToken is
// set, gated behind the same bearer token so the API/event surface isn't
// exposed to the open internet by default. Mirrors iam-org-membership's
// identical registerDocsRoutes for the Swagger half.
func registerDocsRoutes(engine *gin.Engine, docs DocsConfig) {
	if !docs.active() {
		return
	}

	// Defense-in-depth security headers for the docs surface. Swagger UI
	// requires 'unsafe-inline' and 'unsafe-eval' for its bundled JS.
	secHeaders := func(c *gin.Context) {
		c.Header("X-Frame-Options", "DENY")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'unsafe-inline' 'unsafe-eval'; "+
				"style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'")
		c.Next()
	}

	var authMiddleware gin.HandlerFunc = func(c *gin.Context) { c.Next() }
	if docs.Environment == "production" && docs.AuthToken != "" {
		authMiddleware = docsAuthMiddleware(docs.AuthToken)
	}

	stdSwagger := ginSwagger.WrapHandler(swaggerFiles.Handler)
	engine.GET("/swagger/*any", secHeaders, authMiddleware, func(c *gin.Context) {
		switch {
		case strings.HasSuffix(c.Request.URL.Path, "/index.css"):
			SwaggerThemeHandler(c)
		case strings.HasSuffix(c.Request.URL.Path, "/swagger-initializer.js"):
			SwaggerInitializerHandler(c)
		default:
			stdSwagger(c)
		}
	})

	asyncGroup := engine.Group("")
	asyncGroup.Use(secHeaders, authMiddleware)
	asyncGroup.Use(envMiddleware(docs.Environment))
	asyncGroup.GET("/asyncapi", AsyncAPIHandler)
	asyncGroup.GET("/asyncapi.yaml", AsyncAPIYAMLHandler)
}

// docsAuthMiddleware requires an exact `Authorization: Bearer <token>` match
// before letting a request through to the Swagger UI.
func docsAuthMiddleware(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer "+token {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Next()
	}
}

// Handler returns the http.Handler to serve.
func (r *Router) Handler() http.Handler { return r.engine }
