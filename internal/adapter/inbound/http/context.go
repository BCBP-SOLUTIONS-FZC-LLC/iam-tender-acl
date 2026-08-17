package http

import "context"

// RequestContext is this service's own typed view of the gateway-injected
// identity (x-user-id/x-tenant-id/x-tenant-roles). platform-gincommon's own
// RequestContext type lives in an internal package and cannot be
// constructed outside gincommon itself, so ContextBridgeMiddleware
// (middleware.go) copies it into this local type immediately after
// gincommon's ProtectedMiddlewares runs — mirrors iam-user-profile's
// requestctx package. Unit tests construct this type directly.
type RequestContext struct {
	TenantID string
	UserID   string
	Roles    []string
}

// HasAnyRole reports whether rc holds at least one of the given roles.
func (rc *RequestContext) HasAnyRole(roles ...string) bool {
	for _, want := range roles {
		for _, have := range rc.Roles {
			if have == want {
				return true
			}
		}
	}
	return false
}

type requestContextKey struct{}

// WithContext stores rc on ctx.
func WithContext(ctx context.Context, rc *RequestContext) context.Context {
	return context.WithValue(ctx, requestContextKey{}, rc)
}

// RequestContextFromContext retrieves the RequestContext stored by
// WithContext/ContextBridgeMiddleware.
func RequestContextFromContext(ctx context.Context) (*RequestContext, bool) {
	rc, ok := ctx.Value(requestContextKey{}).(*RequestContext)
	return rc, ok
}
