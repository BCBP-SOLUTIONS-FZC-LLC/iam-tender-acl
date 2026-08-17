// Package port declares the interfaces core/service depends on but does
// not implement — persistence (TenderACLRepository), the outbound
// membership check (MembershipCheckClient), and the cache (Cache). Every
// concrete implementation lives under internal/adapter/{inbound,outbound}
// and is wired to these ports only in cmd/tender-acl's composition root,
// so core/service never imports an adapter package directly.
package port

import (
	"context"

	"github.com/google/uuid"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/domain"
)

// TenderACLRepository is the persistence port for tender_acl_entries,
// implemented by internal/adapter/outbound/postgres.TenderACLRepository.
type TenderACLRepository interface {
	// List implements TAC-1: every active-or-not row for a tenant/tender,
	// ordered by created_at.
	List(ctx context.Context, tenantID, tenderID uuid.UUID) ([]domain.TenderACLEntry, error)

	// Grant implements TAC-2's write: entry.ID/RecordVersion/CreatedAt/
	// UpdatedAt are ignored on input and populated from the inserted row.
	// Returns a *domain.Error with ErrCodeDuplicateGrant if the
	// insert would violate uq_tae_active_entry.
	Grant(ctx context.Context, entry domain.TenderACLEntry) (domain.TenderACLEntry, error)

	// Revoke implements TAC-3 exactly as iam-org-membership's original
	// code did: a plain soft-delete with no record_version check (see
	// service.ACLService.Revoke's doc comment). found reports whether an
	// active row existed to revoke — its absence is not an error, matching
	// this table's idempotent-in-effect revoke semantics (LLD §12.2).
	Revoke(ctx context.Context, tenantID, tenderID, userID uuid.UUID) (found bool, err error)

	// FindActive implements the TAE-3 predicate for TAC-4/I-12. Returns
	// (nil, nil) when no active grant exists — this is never an error.
	FindActive(ctx context.Context, tenantID, tenderID, userID uuid.UUID) (*domain.TenderACLEntry, error)

	// CascadeDeleteForTenant implements the tenant-offboarding cascade
	// (LLD §10.1/§11.4/§7.6.4), replacing the lost fk_tae_tenant ON DELETE
	// CASCADE. A hard delete: offboarding is this table's retention
	// terminal point (LLD §18), not another soft-delete. Idempotent by
	// construction — deleting an already-cleaned tenant is a no-op.
	CascadeDeleteForTenant(ctx context.Context, tenantID uuid.UUID) (deleted int64, err error)

	// SoftDeleteForUser implements the per-user-removal ACL cascade
	// (ADR-0007 Wave 3 Phase 3, O_AND_M_DELTA.md §5 Option B), replacing
	// the same-transaction SoftDeleteForUser call iam-org-membership's
	// MembershipService.RemoveUser used to make before this table moved to
	// its own database. Unlike CascadeDeleteForTenant, this is a SOFT
	// delete (sets deleted_at), mirroring both iam-org-membership's
	// original per-user semantics and this table's own retention framing
	// (LLD §18 — revoked rows are kept indefinitely for audit outside of
	// tenant offboarding, which is the only hard-delete terminal point).
	// Idempotent by construction — soft-deleting already-revoked rows is a
	// no-op (the WHERE clause only matches deleted_at IS NULL rows).
	SoftDeleteForUser(ctx context.Context, tenantID, userID uuid.UUID) (deleted int64, err error)
}
