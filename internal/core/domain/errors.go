package domain

import "errors"

// Error codes, matching docs/lld/iam-lld-tender-acl-service.md §20 (Appendix: Error
// Taxonomy) verbatim. The HTTP adapter's respondACLError is the single
// place that maps these to status codes. optimistic_lock_conflict is
// returned by Revoke on a record_version mismatch, per LLD §11.2/§12.1.
const (
	ErrCodeInvalidRequest         = "invalid_request"
	ErrCodeUnauthorized           = "unauthorized"
	ErrCodeInsufficientRole       = "insufficient_role"
	ErrCodeInvalidAccessLevel     = "invalid_access_level"
	ErrCodeInvalidReason          = "invalid_reason"
	ErrCodeInvalidExpiry          = "invalid_expiry"
	ErrCodeGranteeNotActiveMember = "grantee_not_active_member"
	ErrCodeOptimisticLockConflict = "optimistic_lock_conflict"
	ErrCodeDuplicateGrant         = "duplicate_grant"
	ErrCodeCoreUnavailable        = "core_unavailable"
	ErrCodeDependencyUnavailable  = "dependency_unavailable"
	ErrCodeInternal               = "internal_server_error"
)

// Error is a domain-level error carrying a stable, machine-readable code so
// the HTTP adapter can translate it to the correct status and response body
// without inspecting error strings. iam-org-membership's own domain package
// uses the wrapped-sentinel-plus-Cause DomainError shape instead — this
// service's error taxonomy is flat enough that a bare code+message has
// always been sufficient, and "domain.Error" avoids the type-name stutter
// "domain.DomainError" would otherwise produce.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// NewError constructs an Error with the given code and message.
func NewError(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// CodeOf extracts the error code from err, if any. It walks the error chain
// via errors.As, so wrapped errors are still recognized.
func CodeOf(err error) (string, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Code, true
	}
	return "", false
}
