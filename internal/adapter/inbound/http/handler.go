package http

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/domain"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/service"
	gincommon "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-gincommon/pkg/gincommon"
	pgcommon "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/pkg/pgcommon"
)

// Handler implements TAC-1/TAC-2/TAC-3 (public, role-gated) and
// TAC-4/I-12 (internal, mesh-only — see internal_handler.go).
type Handler struct {
	svc *service.ACLService
}

// NewHandler builds a Handler.
func NewHandler(svc *service.ACLService) *Handler { return &Handler{svc: svc} }

func parseUUIDParam(c *gin.Context, name string) (uuid.UUID, bool) {
	v, err := uuid.Parse(c.Param(name))
	if err != nil {
		writeError(c, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
		return uuid.UUID{}, false
	}
	return v, true
}

// requireSameTenant enforces that the caller's gateway-injected tenant
// matches the :id path parameter AND that the caller holds one of
// requiredRoles — defense in depth alongside RLS, matching
// iam-org-membership's requireSameTenantMember+requireTenderAdminOrHigher
// pattern. TAC-1/2/3 are resolved entirely from x-tenant-roles + this
// check, with no DB round trip (LLD §8.2).
//
// This check is deliberately NOT delegated to platform-gincommon's
// RequirePermission middleware: that middleware requires a port.Authorizer
// and responds via its own generic denial path, which does not match
// docs/lld/iam-lld-tender-acl-service.md §20's single insufficient_role code for every
// authz-boundary failure on these routes (tenant mismatch or role
// mismatch alike).
func requireSameTenant(c *gin.Context, tenantID uuid.UUID) bool {
	rc, ok := RequestContextFromContext(c.Request.Context())
	if !ok || rc.TenantID != tenantID.String() || !rc.HasAnyRole(requiredRoles...) {
		writeError(c, http.StatusForbidden, domain.ErrCodeInsufficientRole)
		return false
	}
	return true
}

// writeError writes this service's standard error envelope
// (ErrorResponse, dto.go), populating trace_id/request_id from
// gincommon's own TracingMiddleware/RequestIDMiddleware-populated gin
// context — the same values gincommon's own middleware-emitted error
// bodies use — rather than re-deriving trace_id from the raw OTel span
// (a prior version did, duplicating what gincommon.TraceIDFromContext
// already exposes).
func writeError(c *gin.Context, status int, code string) {
	resp := ErrorResponse{
		Error:     code,
		Status:    status,
		TraceID:   gincommon.TraceIDFromContext(c),
		RequestID: gincommon.RequestIDFromContext(c),
	}
	c.AbortWithStatusJSON(status, resp)
}

// respondACLError translates a domain.Error's code into the correct
// HTTP status per docs/lld/iam-lld-tender-acl-service.md §20, and writes the standard
// error envelope. Errors with no recognized code are treated as
// internal_server_error, never leaking implementation detail.
func respondACLError(c *gin.Context, err error) {
	code, ok := domain.CodeOf(err)
	if !ok {
		// Raw pgconn.PgError that wrapConnErr missed. Maps SQLSTATE
		// 08/53/57/58 to 503 dependency_unavailable without importing
		// pgconn — same fallback iam-org-membership / iam-realm-provisioner
		// HandleError uses.
		if pgcommon.IsConnectionException(err) || pgcommon.IsInsufficientResources(err) || isOperatorOrSystemErrorSQLState(err) {
			writeError(c, http.StatusServiceUnavailable, domain.ErrCodeDependencyUnavailable)
			return
		}
		writeError(c, http.StatusInternalServerError, domain.ErrCodeInternal)
		return
	}

	status := http.StatusInternalServerError
	switch code {
	case domain.ErrCodeInvalidRequest:
		status = http.StatusBadRequest
	case domain.ErrCodeUnauthorized:
		status = http.StatusUnauthorized
	case domain.ErrCodeInsufficientRole:
		status = http.StatusForbidden
	case domain.ErrCodeInvalidAccessLevel, domain.ErrCodeInvalidReason, domain.ErrCodeInvalidExpiry, domain.ErrCodeGranteeNotActiveMember:
		status = http.StatusUnprocessableEntity
	case domain.ErrCodeOptimisticLockConflict, domain.ErrCodeDuplicateGrant:
		status = http.StatusConflict
	case domain.ErrCodeCoreUnavailable, domain.ErrCodeDependencyUnavailable:
		status = http.StatusServiceUnavailable
	}
	writeError(c, status, code)
}

// isOperatorOrSystemErrorSQLState reports whether err is a Postgres error
// in SQLSTATE class 57 or 58. pgcommon v1.3.0 has dedicated helpers for
// 08/53 but not these two; we classify via the pgconn Error() text so
// this inbound package never imports pgconn. Mirrors iam-org-membership /
// iam-realm-provisioner's identical HandleError helper.
func isOperatorOrSystemErrorSQLState(err error) bool {
	if !pgcommon.IsPgError(err) {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLSTATE 57") || strings.Contains(msg, "SQLSTATE 58")
}

// maxRequestBodyBytes bounds TAC-2/TAC-3's request body — neither request
// shape (GrantRequest/RevokeRequest) legitimately needs more than a few
// hundred bytes; this is defense-in-depth against an oversized body being
// read in full before ShouldBindJSON ever gets to reject it, independent of
// whatever cap the gateway/mesh in front of this service applies.
const maxRequestBodyBytes = 16 * 1024

// limitRequestBody wraps the request body in http.MaxBytesReader so a body
// exceeding maxRequestBodyBytes fails during ShouldBindJSON's read instead
// of being buffered in full first.
func limitRequestBody(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBodyBytes)
}

// defaultListLimit/maxListLimit bound TAC-1's page size: unbounded listing
// let a tender with a large ACL history return every row in one response.
// defaultListLimit applies when the caller omits ?limit; maxListLimit is a
// hard ceiling regardless of what the caller requests.
const (
	defaultListLimit = 100
	maxListLimit     = 500
)

// parseListPagination reads ?limit=&offset= from the query string, applying
// defaultListLimit/clamping to maxListLimit and rejecting a negative or
// non-integer value as invalid_request. Returns ok=false after already
// writing the error response.
func parseListPagination(c *gin.Context) (limit, offset int, ok bool) {
	limit = defaultListLimit
	if raw := c.Query("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 {
			writeError(c, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
			return 0, 0, false
		}
		limit = v
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}

	offset = 0
	if raw := c.Query("offset"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			writeError(c, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
			return 0, 0, false
		}
		offset = v
	}
	return limit, offset, true
}

// List implements TAC-1.
//
// @Summary      TAC-1 — List tender ACL entries
// @Description  Every active-or-not entry for a tender, ordered by created_at, paginated (default limit 100, max 500). Not cached.
// @Tags         public
// @Produce      json
// @Param        id         path      string  true   "Tenant UUID"  format(uuid)
// @Param        tender_id  path      string  true   "Tender UUID"  format(uuid)
// @Param        limit      query     int     false  "Page size (default 100, max 500)"
// @Param        offset     query     int     false  "Rows to skip (default 0)"
// @Success      200        {object}  ListResponse
// @Failure      400        {object}  ErrorResponse
// @Failure      403        {object}  ErrorResponse
// @Failure      503        {object}  ErrorResponse
// @Security     UserID
// @Security     TenantID
// @Security     TenantRoles
// @Router       /api/v1/tenants/{id}/tenders/{tender_id}/acl [get]
func (h *Handler) List(c *gin.Context) {
	tenantID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if !requireSameTenant(c, tenantID) {
		return
	}
	tenderID, ok := parseUUIDParam(c, "tender_id")
	if !ok {
		return
	}
	limit, offset, ok := parseListPagination(c)
	if !ok {
		return
	}

	entries, err := h.svc.List(c.Request.Context(), tenantID, tenderID, limit, offset)
	if err != nil {
		respondACLError(c, err)
		return
	}
	c.JSON(http.StatusOK, ListResponse{Entries: toACLResponses(entries), Limit: limit, Offset: offset})
}

// Grant implements TAC-2.
//
// @Summary      TAC-2 — Grant a tender ACL entry
// @Description  Blocks on a synchronous membership-existence check against iam-org-membership — fails closed (503 core_unavailable) if that check can't be performed.
// @Tags         public
// @Accept       json
// @Produce      json
// @Param        id         path      string        true  "Tenant UUID"  format(uuid)
// @Param        tender_id  path      string        true  "Tender UUID"  format(uuid)
// @Param        body       body      GrantRequest  true  "Grant request"
// @Success      201        {object}  ACLResponse
// @Failure      400        {object}  ErrorResponse
// @Failure      401        {object}  ErrorResponse
// @Failure      403        {object}  ErrorResponse
// @Failure      409        {object}  ErrorResponse
// @Failure      422        {object}  ErrorResponse
// @Failure      503        {object}  ErrorResponse
// @Security     UserID
// @Security     TenantID
// @Security     TenantRoles
// @Router       /api/v1/tenants/{id}/tenders/{tender_id}/acl [post]
func (h *Handler) Grant(c *gin.Context) {
	tenantID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if !requireSameTenant(c, tenantID) {
		return
	}
	tenderID, ok := parseUUIDParam(c, "tender_id")
	if !ok {
		return
	}

	limitRequestBody(c)
	var req GrantRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
		return
	}
	userID, err := uuid.Parse(req.UserID)
	if err != nil {
		writeError(c, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
		return
	}

	rc, ok := RequestContextFromContext(c.Request.Context())
	var grantedBy uuid.UUID
	if ok {
		grantedBy, err = uuid.Parse(rc.UserID)
	}
	if !ok || err != nil {
		writeError(c, http.StatusUnauthorized, domain.ErrCodeUnauthorized)
		return
	}

	reason := ""
	if req.Reason != nil {
		reason = *req.Reason
	}

	entry, err := h.svc.Grant(c.Request.Context(), tenantID, tenderID, userID, grantedBy, domain.TenderACLLevel(req.AccessLevel), reason, req.ExpiresAt)
	if err != nil {
		respondACLError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toACLResponse(entry))
}

// Revoke implements TAC-3.
//
// @Summary      TAC-3 — Revoke a tender ACL entry
// @Description  Soft-delete, optimistic-locked on the caller's last-read record_version (LLD §11.2/§12.1) — a version mismatch, including against an already-revoked row, returns 409 optimistic_lock_conflict.
// @Tags         public
// @Accept       json
// @Param        id         path  string         true  "Tenant UUID"  format(uuid)
// @Param        tender_id  path  string         true  "Tender UUID"  format(uuid)
// @Param        user_id    path  string         true  "User UUID"    format(uuid)
// @Param        body       body  RevokeRequest  true  "Last-read record_version"
// @Success      204
// @Failure      400  {object}  ErrorResponse
// @Failure      403  {object}  ErrorResponse
// @Failure      409  {object}  ErrorResponse
// @Failure      503  {object}  ErrorResponse
// @Security     UserID
// @Security     TenantID
// @Security     TenantRoles
// @Router       /api/v1/tenants/{id}/tenders/{tender_id}/acl/{user_id} [delete]
func (h *Handler) Revoke(c *gin.Context) {
	tenantID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}
	if !requireSameTenant(c, tenantID) {
		return
	}
	tenderID, ok := parseUUIDParam(c, "tender_id")
	if !ok {
		return
	}
	userID, ok := parseUUIDParam(c, "user_id")
	if !ok {
		return
	}

	limitRequestBody(c)
	var req RevokeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, domain.ErrCodeInvalidRequest)
		return
	}

	if err := h.svc.Revoke(c.Request.Context(), tenantID, tenderID, userID, req.RecordVersion); err != nil {
		respondACLError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
