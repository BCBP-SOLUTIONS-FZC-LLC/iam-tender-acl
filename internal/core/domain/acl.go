// Package domain holds this service's pure business types — no DB, no
// HTTP, no outbound clients. Clean Architecture layering (core/domain |
// core/port | core/service, adapter/inbound | adapter/outbound), matching
// every sibling IAM service (iam-org-membership, iam-catalog-admin,
// iam-group-mapping). This supersedes the flat, deliberately unlayered
// layout this service originally shipped with (tender-acl-service-lld.md
// §6, decision TAC-D1) — see ARCHITECTURE.md's "Layer model" section for
// the history of that divergence and why it was reversed.
package domain

import (
	"time"

	"github.com/google/uuid"
)

// TenderACLLevel is the access level granted by a tender_acl_entries row.
type TenderACLLevel string

// The three levels the tender_acl_level enum accepts (§16 A17/A32(c)).
const (
	ACLView    TenderACLLevel = "view"
	ACLEdit    TenderACLLevel = "edit"
	ACLApprove TenderACLLevel = "approve"
)

// Valid reports whether l is one of the three levels the tender_acl_level
// enum accepts.
func (l TenderACLLevel) Valid() bool {
	switch l {
	case ACLView, ACLEdit, ACLApprove:
		return true
	default:
		return false
	}
}

// TenderACLEntry is a single restricted-tender access grant, lifted
// near-verbatim from iam-org-membership's (now-deleted) internal/core/
// domain/tender_acl.go. TenantMembershipID was the anchor for a composite
// FK (fk_tae_tenant_membership) that could not survive this table moving
// to its own database (LLD §7.6) — the field remains, populated at
// grant time via the membershipcheck round trip, but is no longer
// DB-validated against a live tenant_memberships row.
type TenderACLEntry struct {
	ID                 uuid.UUID
	TenantID           uuid.UUID
	TenderID           uuid.UUID
	UserID             uuid.UUID
	TenantMembershipID uuid.UUID
	AccessLevel        TenderACLLevel
	GrantedBy          uuid.UUID
	Reason             string
	ExpiresAt          *time.Time
	RecordVersion      int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
	DeletedAt          *time.Time
}

// IsActive implements the TAE-3 predicate: deleted_at IS NULL AND
// (expires_at IS NULL OR expires_at > now()). This predicate was never
// conditioned on live membership status (LLD §7.6.3) — that is what makes
// the lost composite FK a grant-time-only safety net rather than a
// read-time authorization input.
func (e *TenderACLEntry) IsActive(now time.Time) bool {
	if e.DeletedAt != nil {
		return false
	}
	if e.ExpiresAt != nil && !e.ExpiresAt.After(now) {
		return false
	}
	return true
}

// CachedAccess is the result of a TAC-4/CheckAccess lookup — both the
// value Service.CheckAccess returns and the JSON wire format stored under
// a tac:acl:* cache key (LLD §9).
type CachedAccess struct {
	HasAccess   bool       `json:"has_access"`
	AccessLevel *string    `json:"access_level,omitempty"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}
