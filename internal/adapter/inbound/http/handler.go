package http

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/domain"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/service"
	gincommon "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-gincommon/pkg/gincommon"
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
// tender-acl-service-lld.md §20's single insufficient_role code for every
// authz-boundary failure on these routes (tenant mismatch or role
// mismatch alike). See IMPLEMENTATION_GAP_ANALYSIS.md.
func requireSameTenant(c *gin.Context, tenantID uuid.UUID) bool {
	rc, ok := RequestContextFromContext(c.Request.Context())
	if !ok || rc.TenantID != tenantID.String() || !rc.HasAnyRole(requiredRoles...) {
		writeError(c, http.StatusForbidden, domain.ErrCodeInsufficientRole)
		return false
	}
	return true
}

// writeError writes this service's standard error envelope
// (ErrorResponse, dto.go), populating trace_id/request_id from the
// current span/gincommon request-id the same way gincommon's own
// middleware-emitted error bodies do.
func writeError(c *gin.Context, status int, code string) {
	resp := ErrorResponse{Error: code, Status: status}
	if span := trace.SpanFromContext(c.Request.Context()); span.SpanContext().IsValid() {
		resp.TraceID = span.SpanContext().TraceID().String()
	}
	resp.RequestID = gincommon.RequestIDFromContext(c)
	c.AbortWithStatusJSON(status, resp)
}

// respondACLError translates a domain.Error's code into the correct
// HTTP status per tender-acl-service-lld.md §20, and writes the standard
// error envelope. Errors with no recognized code are treated as
// internal_server_error, never leaking implementation detail.
func respondACLError(c *gin.Context, err error) {
	code, ok := domain.CodeOf(err)
	if !ok {
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

// List implements TAC-1.
//
// @Summary      TAC-1 — List tender ACL entries
// @Description  Every active-or-not entry for a tender, ordered by created_at. Not cached.
// @Tags         public
// @Produce      json
// @Param        id         path      string  true  "Tenant UUID"  format(uuid)
// @Param        tender_id  path      string  true  "Tender UUID"  format(uuid)
// @Success      200        {object}  ListResponse
// @Failure      400        {object}  ErrorResponse
// @Failure      403        {object}  ErrorResponse
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

	entries, err := h.svc.List(c.Request.Context(), tenantID, tenderID)
	if err != nil {
		respondACLError(c, err)
		return
	}
	c.JSON(http.StatusOK, ListResponse{Entries: toACLResponses(entries)})
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
// @Description  Soft-delete, idempotent in effect — revoking an already-revoked (or never-granted) entry is not an error.
// @Tags         public
// @Param        id         path  string  true  "Tenant UUID"  format(uuid)
// @Param        tender_id  path  string  true  "Tender UUID"  format(uuid)
// @Param        user_id    path  string  true  "User UUID"    format(uuid)
// @Success      204
// @Failure      400  {object}  ErrorResponse
// @Failure      403  {object}  ErrorResponse
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

	if err := h.svc.Revoke(c.Request.Context(), tenantID, tenderID, userID); err != nil {
		respondACLError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
