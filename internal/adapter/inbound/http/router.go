package http

import (
	"log/slog"
	"net/http"

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

// slogPlatformLogger adapts *slog.Logger to the map[string]interface{}-based
// Logger interface both platform-gincommon's and platform-events' Config
// structs expect (identical method shape in both libraries, structurally
// satisfied here without importing either library's internal port package).
type slogPlatformLogger struct{ l *slog.Logger }

func (a slogPlatformLogger) Debug(msg string, fields map[string]interface{}) {
	a.l.Debug(msg, mapToArgs(fields)...)
}
func (a slogPlatformLogger) Info(msg string, fields map[string]interface{}) {
	a.l.Info(msg, mapToArgs(fields)...)
}
func (a slogPlatformLogger) Warn(msg string, fields map[string]interface{}) {
	a.l.Warn(msg, mapToArgs(fields)...)
}
func (a slogPlatformLogger) Error(msg string, fields map[string]interface{}) {
	a.l.Error(msg, mapToArgs(fields)...)
}

func mapToArgs(fields map[string]interface{}) []any {
	args := make([]any, 0, len(fields)*2)
	for k, v := range fields {
		args = append(args, k, v)
	}
	return args
}

// NewRouter wires every route: the public admin API (TAC-1/2/3, role-gated
// via RequestContext.Roles), the internal mTLS-only access-check API
// (TAC-4, no RBAC — tenant isolation from RLS alone), and the health
// checks.
func NewRouter(h *Handler, postgres PostgresHealth, cache Pinger, logger *slog.Logger, tracing *gincommon.TracingOptions, docs DocsConfig) *Router {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()

	platformLogger := slogPlatformLogger{l: logger}
	cfg := gincommon.Config{Logger: platformLogger, ServiceName: "tender-acl", Tracing: tracing}
	// Observability (recovery/request-id/tracing/metrics/correlation/logging)
	// applies to every route, including TAC-4. Auth (ProtectedMiddlewares)
	// applies only to the public admin group below — TAC-4 is a mesh-only
	// trust boundary with no RBAC/JWT check (LLD §8.2/§13.2).
	//
	// Tracing comes entirely from gincommon's own TracingMiddleware here
	// (part of ObservabilityMiddlewares), which lazily installs a real OTel
	// TracerProvider (via cfg.Tracing) the first time this function runs —
	// NOT a separate otelgin.Middleware or a hand-rolled TracerProvider in
	// cmd/tender-acl, both since removed as redundant with what gincommon
	// already provides.
	//
	// Generic per-request HTTP metrics (count/duration/status, by
	// method+route) are gincommon's own http_requests_total/
	// http_request_duration_seconds (also part of ObservabilityMiddlewares'
	// MetricsMiddleware) — passed through as-is, not duplicated by a local
	// metricsMiddleware. This service is not yet deployed, so there was no
	// live dependency on the old tender_acl_requests_total/
	// tender_acl_request_duration_seconds names to preserve; deploy/monitoring's
	// SLO/alert/HPA rules and the release canary check were updated to the
	// gincommon names in the same change. internal/adapter/outbound/metrics
	// still owns every metric gincommon has no equivalent for — writes,
	// grant checks, cache hits/misses, and both cascades.
	engine.Use(gincommon.ObservabilityMiddlewares(cfg)...)

	public := engine.Group("/api/v1/tenants/:id/tenders/:tender_id/acl")
	public.Use(gincommon.ProtectedMiddlewares(cfg)...)
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
	group := engine.Group("/swagger")
	if docs.Environment == "production" && docs.AuthToken != "" {
		group.Use(docsAuthMiddleware(docs.AuthToken))
	}
	group.GET("/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	asyncGroup := engine.Group("")
	if docs.Environment == "production" && docs.AuthToken != "" {
		asyncGroup.Use(docsAuthMiddleware(docs.AuthToken))
	}
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
