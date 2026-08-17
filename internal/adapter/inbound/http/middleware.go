package http

import (
	"github.com/gin-gonic/gin"

	gincommon "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-gincommon/pkg/gincommon"
)

// ContextBridgeMiddleware must run after gincommon.ProtectedMiddlewares
// (which populates gincommon's own, unexported-type RequestContext). It
// copies the identity into this package's own RequestContext (context.go)
// so the rest of the service — and unit tests — never need to reference
// gincommon's internal type directly.
func ContextBridgeMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if prc, ok := gincommon.RequestContext(c); ok {
			rc := &RequestContext{TenantID: prc.TenantID, UserID: prc.UserID, Roles: prc.Roles}
			c.Request = c.Request.WithContext(WithContext(c.Request.Context(), rc))
		}
		c.Next()
	}
}
