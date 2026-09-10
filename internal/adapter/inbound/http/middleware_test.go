package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

// TestContextBridgeMiddleware_NoGincommonContext_NextStillCalled covers the
// case where ContextBridgeMiddleware runs without gincommon's own
// RequestContext having been populated first (e.g. wired without
// ProtectedMiddlewares ahead of it, or on a request gincommon itself
// rejected before reaching here in a real chain). It must not panic and
// must still call c.Next() — this package's own RequestContext is simply
// left unset rather than a bridge failure aborting the request.
func TestContextBridgeMiddleware_NoGincommonContext_NextStillCalled(t *testing.T) {
	engine := gin.New()
	var nextCalled bool
	var bridgedRC *RequestContext
	engine.Use(ContextBridgeMiddleware())
	engine.GET("/", func(c *gin.Context) {
		nextCalled = true
		bridgedRC, _ = RequestContextFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	engine.ServeHTTP(w, req)

	assert.True(t, nextCalled, "ContextBridgeMiddleware must call c.Next() even with no gincommon RequestContext present")
	assert.Nil(t, bridgedRC, "no gincommon RequestContext to bridge → this package's RequestContext stays unset")
	assert.Equal(t, http.StatusOK, w.Code)
}
