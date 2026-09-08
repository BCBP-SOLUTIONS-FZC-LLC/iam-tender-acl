package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
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
