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
