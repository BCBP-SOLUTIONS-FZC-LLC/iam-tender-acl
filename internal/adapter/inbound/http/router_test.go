package http

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// ── slogPlatformLogger ────────────────────────────────────────────────────────

func TestSlogPlatformLogger_AllLevels_DoNotPanic(t *testing.T) {
	l := slogPlatformLogger{l: slog.Default()}
	fields := map[string]interface{}{"key": "value", "count": 1}
	assert.NotPanics(t, func() { l.Debug("debug msg", fields) })
	assert.NotPanics(t, func() { l.Info("info msg", fields) })
	assert.NotPanics(t, func() { l.Warn("warn msg", fields) })
	assert.NotPanics(t, func() { l.Error("error msg", fields) })
}

// ── mapToArgs ─────────────────────────────────────────────────────────────────

func TestMapToArgs_EmptyMap_ReturnsEmptySlice(t *testing.T) {
	args := mapToArgs(map[string]interface{}{})
	assert.Empty(t, args)
}

func TestMapToArgs_WithFields_ReturnsKeyValuePairs(t *testing.T) {
	args := mapToArgs(map[string]interface{}{"k": "v"})
	assert.Len(t, args, 2)
	// args is [key, value, ...] — order not guaranteed for maps, just check length
}

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
