package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestSwaggerThemeHandler_Returns200CSS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/swagger/index.css", http.NoBody)

	SwaggerThemeHandler(c)

	assert.Equal(t, 200, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/css")
	body := w.Body.String()
	// The theme CSS includes @import and body rules.
	assert.True(t,
		contains(body, "@import") || contains(body, "body"),
		"expected CSS content in response body",
	)
}

func TestSwaggerInitializerHandler_Returns200JS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/swagger/swagger-initializer.js", http.NoBody)

	SwaggerInitializerHandler(c)

	assert.Equal(t, 200, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "javascript")
	assert.Contains(t, w.Body.String(), "SwaggerUIBundle")
}

// contains is a small helper to avoid importing strings in a test file that
// already uses gin/testify — keeps the import list tidy.
func contains(s, sub string) bool {
	return len(s) >= len(sub) && func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}()
}
