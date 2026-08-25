package port

import (
	"context"

	"github.com/google/uuid"
)

// MembershipCheckClient reports whether a user holds an active tenant
// membership. It is consulted only at TAC-2 (grant) time — TAC-1/TAC-3/
// TAC-4 have no dependency on it (LLD §7.6.3/TAC-D3). Implemented by
// internal/adapter/outbound/membershipcheck.HTTPChecker; this is the
// swappable client port replacing the composite FK this table lost when
// it moved out of Core's database (tender-acl-service-lld.md §6, §7.6.2).
// It exists specifically so Wave 4 (folding this service into the Tender
// Service, ADR-0007 Option D) can repoint or delete this dependency
// cheaply.
//
// When active is true, membershipID is the tenant_membership_id to store
// as an audit-only reference on the granted row. tender_acl_entries.
// tenant_membership_id is NOT NULL, so the provider contract (LLD §7.6.2,
// TAC-D11) returns it alongside active rather than a bare bool.
//
// err is non-nil only when the check itself could not be performed
// (network error, timeout, non-2xx status) — never as a way of expressing
// "not active". Callers must fail CLOSED on err (block the grant, surface
// 503 core_unavailable), never fail-open (TAC-FAIL-1).
type MembershipCheckClient interface {
	Exists(ctx context.Context, tenantID, userID uuid.UUID) (active bool, membershipID uuid.UUID, err error)
}
