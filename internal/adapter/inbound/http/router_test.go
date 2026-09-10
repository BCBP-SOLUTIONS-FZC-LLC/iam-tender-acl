package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/domain"
	gincommon "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-gincommon/pkg/gincommon"
)

// ── Swagger docs routes (SEC-SWAGGER-01/02) ───────────────────────────────────

// TestSwagger_ProductionDisabled_404 guards SEC-SWAGGER-01: when
// APP_ENV=production and DOCS_ENABLED=false, DocsConfig.active() returns
// false → registerDocsRoutes is a no-op → /swagger/* 404.
func TestSwagger_ProductionDisabled_404(t *testing.T) {
	engine := gin.New()
	registerDocsRoutes(engine, DocsConfig{Environment: "production", Enabled: false})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/swagger/index.html", http.NoBody)
	engine.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestSwagger_WrongToken_401 guards SEC-SWAGGER-02: production + Enabled=true
// but bearer token does not match → docsAuthMiddleware returns 401.
// ── NewRouter / Handler() — full wiring ───────────────────────────────────────

// TestNewRouter_HealthzAndReadyz_Wired confirms NewRouter actually wires the
// health checks and returns a usable http.Handler via Handler() — the same
// construction path cmd/tender-acl/main.go and test/e2e/main_test.go use,
// exercised here with fakes so it participates in unit-suite coverage too
// (e2e is a separate build tag, not merged into make test-ci's profile).
func TestNewRouter_HealthzAndReadyz_Wired(t *testing.T) {
	h := newTestHandler(emptyRepo(), &fakeChecker{}, &fakeCache{}, t)
	postgres := fakePostgresHealth{healthy: true}
	cache := fakePinger{}

	router := NewRouter(h, postgres, cache, gincommon.Config{}, DocsConfig{Environment: "development"})
	require.NotNil(t, router)
	handler := router.Handler()
	require.NotNil(t, handler)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", http.NoBody)
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/readyz", http.NoBody)
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestNewRouter_TAC4Route_Wired confirms the internal (mesh-only, no-RBAC)
// TAC-4 route is registered and reachable — this route has no auth
// middleware in front of it, so it's exercisable without needing gincommon's
// real ProtectedMiddlewares to populate a RequestContext first.
func TestNewRouter_TAC4Route_Wired(t *testing.T) {
	repo := &fakeRepo{
		findActiveFn: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.TenderACLEntry, error) {
			return nil, nil
		},
	}
	h := newTestHandler(repo, &fakeChecker{}, &fakeCache{}, t)
	router := NewRouter(h, fakePostgresHealth{healthy: true}, fakePinger{}, gincommon.Config{}, DocsConfig{Environment: "development"})

	tenantID, tenderID, userID := uuid.New(), uuid.New(), uuid.New()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/tenants/"+tenantID.String()+"/tenders/"+tenderID.String()+"/acl/"+userID.String(), http.NoBody)
	router.Handler().ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestSwagger_WrongToken_401(t *testing.T) {
	engine := gin.New()
	registerDocsRoutes(engine, DocsConfig{Environment: "production", Enabled: true, AuthToken: "secret-token"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/swagger/index.html", http.NoBody)
	req.Header.Set("Authorization", "Bearer wrong-token")
	engine.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// ── Swagger sub-route branch coverage ─────────────────────────────────────────

// TestSwagger_IndexCSS_ServesTheme hits the index.css branch inside the
// wildcard /swagger/*any handler, confirming SwaggerThemeHandler is wired.
func TestSwagger_IndexCSS_ServesTheme(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	registerDocsRoutes(engine, DocsConfig{Environment: "development"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/swagger/index.css", http.NoBody)
	engine.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/css")
}

// TestSwagger_InitializerJS_ServesInitializer hits the swagger-initializer.js
// branch, confirming SwaggerInitializerHandler is wired.
func TestSwagger_InitializerJS_ServesInitializer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	registerDocsRoutes(engine, DocsConfig{Environment: "development"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/swagger/swagger-initializer.js", http.NoBody)
	engine.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "javascript")
}

// ── Router.Handler() ──────────────────────────────────────────────────────────

// TestRouter_Handler_ReturnsHTTPHandler verifies that Router.Handler() returns
// a non-nil http.Handler without panic.
func TestRouter_Handler_ReturnsHTTPHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := &Router{engine: gin.New()}
	h := r.Handler()
	assert.NotNil(t, h)
}

// ── ContextBridgeMiddleware (no gincommon context path) ───────────────────────

// TestContextBridgeMiddleware_NoGincommonContext_PassesThrough verifies that
// when gincommon.RequestContext returns ok=false (no ProtectedMiddlewares ran),
// the middleware simply calls Next() without setting a RequestContext, leaving
// the downstream handler reachable.
func TestContextBridgeMiddleware_NoGincommonContext_PassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reached := false
	engine := gin.New()
	engine.Use(ContextBridgeMiddleware())
	engine.GET("/test", func(c *gin.Context) {
		reached = true
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
	engine.ServeHTTP(w, req)
	assert.True(t, reached)
	assert.Equal(t, http.StatusOK, w.Code)
}

// TestSwagger_DefaultRoute_ServesSwaggerUI exercises the default branch in
// the /swagger/*any switch (not index.css, not swagger-initializer.js).
func TestSwagger_DefaultRoute_ServesSwaggerUI(t *testing.T) {
	engine := gin.New()
	registerDocsRoutes(engine, DocsConfig{Environment: "development"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/swagger/index.html", http.NoBody)
	engine.ServeHTTP(w, req)
	assert.NotEqual(t, http.StatusNotFound, w.Code)
}
