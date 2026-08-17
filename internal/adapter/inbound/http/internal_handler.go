package http

import "net/http"

import "github.com/gin-gonic/gin"

// CheckAccess implements TAC-4/I-12 — mesh-only, mTLS trust boundary: no
// RBAC, no JWT parsing (LLD §8.2/§13.2). Response shape and "never 404"
// contract are byte-identical to iam-org-membership's original
// CheckTenderAccess.
//
// @Summary      TAC-4 — Check tender access
// @Description  Mesh-only, no RBAC/JWT check at all. Never 404 — a missing grant is a valid, cacheable has_access:false answer. Cached 30s in Valkey.
// @Tags         internal
// @Produce      json
// @Param        id         path      string  true  "Tenant UUID"  format(uuid)
// @Param        tender_id  path      string  true  "Tender UUID"  format(uuid)
// @Param        user_id    path      string  true  "User UUID"    format(uuid)
// @Success      200        {object}  CheckAccessResponse
// @Failure      400        {object}  ErrorResponse
// @Router       /internal/tenants/{id}/tenders/{tender_id}/acl/{user_id} [get]
func (h *Handler) CheckAccess(c *gin.Context) {
	tenantID, ok := parseUUIDParam(c, "id")
	if !ok {
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

	result, err := h.svc.CheckAccess(c.Request.Context(), tenantID, tenderID, userID)
	if err != nil {
		respondACLError(c, err)
		return
	}
	c.JSON(http.StatusOK, CheckAccessResponse{
		HasAccess:   result.HasAccess,
		AccessLevel: result.AccessLevel,
		ExpiresAt:   result.ExpiresAt,
	})
}
