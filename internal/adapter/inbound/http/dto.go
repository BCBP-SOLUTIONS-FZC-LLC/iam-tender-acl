// Package http is the inbound HTTP adapter: TAC-1/2/3 (public, role-gated),
// TAC-4/I-12 (internal, mesh-only), health checks, and the DTO/error-code
// translation between the wire and internal/core/service's business types.
package http

import (
	"time"

	"github.com/google/uuid"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/domain"
)

// ErrorResponse is this service's wire-format error body. It mirrors
// platform-gincommon's pkg/gincommon.ErrorResponse shape byte-for-byte (same
// field names/JSON tags) but is declared locally: gincommon has no
// RespondError-style helper to construct one, so writeError (handler.go)
// builds this directly.
type ErrorResponse struct {
	Error     string `json:"error"`
	Status    int    `json:"status"`
	TraceID   string `json:"trace_id"`
	RequestID string `json:"request_id"`
}

// GrantRequest is TAC-2's request body.
type GrantRequest struct {
	UserID      string     `json:"user_id" binding:"required"`
	AccessLevel string     `json:"access_level" binding:"required"`
	Reason      *string    `json:"reason,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

// ACLResponse is the wire shape of a single tender_acl_entries row, used by
// TAC-1 and TAC-2.
type ACLResponse struct {
	ID            uuid.UUID  `json:"id"`
	TenantID      uuid.UUID  `json:"tenant_id"`
	TenderID      uuid.UUID  `json:"tender_id"`
	UserID        uuid.UUID  `json:"user_id"`
	AccessLevel   string     `json:"access_level"`
	GrantedBy     uuid.UUID  `json:"granted_by"`
	Reason        string     `json:"reason,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	RecordVersion int64      `json:"record_version"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// ListResponse is TAC-1's response body.
type ListResponse struct {
	Entries []ACLResponse `json:"entries"`
}

// CheckAccessResponse is TAC-4/I-12's response body — has_access/
// access_level/expires_at, byte-identical to iam-org-membership's original
// contract. Never a 404: has_access:false is itself the valid "no active
// grant" answer.
type CheckAccessResponse struct {
	HasAccess   bool       `json:"has_access"`
	AccessLevel *string    `json:"access_level,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

func toACLResponse(e domain.TenderACLEntry) ACLResponse {
	return ACLResponse{
		ID:            e.ID,
		TenantID:      e.TenantID,
		TenderID:      e.TenderID,
		UserID:        e.UserID,
		AccessLevel:   string(e.AccessLevel),
		GrantedBy:     e.GrantedBy,
		Reason:        e.Reason,
		ExpiresAt:     e.ExpiresAt,
		RecordVersion: e.RecordVersion,
		CreatedAt:     e.CreatedAt,
		UpdatedAt:     e.UpdatedAt,
	}
}

func toACLResponses(entries []domain.TenderACLEntry) []ACLResponse {
	out := make([]ACLResponse, len(entries))
	for i, e := range entries {
		out[i] = toACLResponse(e)
	}
	return out
}
