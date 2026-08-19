// Package postgres implements port.TenderACLRepository against PostgreSQL
// via pgx/v5 and platform-pgcommon's tenant-scoped transaction helper.
// Every tenant-scoped statement runs inside a transaction opened by
// withTenant (below), which binds app.tenant_id as a transaction-local GUC
// for that transaction only, so Row-Level Security is enforced on every
// access path with no exceptions.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/domain"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/port"
	pgdomain "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/pkg/domain"
	pgcommon "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/pkg/pgcommon"
)

// withTenant runs fn inside a transaction with app.tenant_id bound as a
// transaction-local GUC for tenantID, via pgcommon.RunInTx's PgBouncer-mode
// GUC injection (pool.go/tx.go — see platform-pgcommon). The GUC is set
// directly from tenantID here, independent of any gincommon RequestContext,
// so this works identically whether the caller is an HTTP handler or the
// tenant-offboarding consumer (which has no HTTP request at all).
func withTenant(ctx context.Context, pool *pgcommon.Pool, tenantID uuid.UUID, fn func(context.Context, pgx.Tx) error) error {
	ctx = pgcommon.WithGUCSet(ctx, pgdomain.GUCSet{TenantID: tenantID.String()})
	return wrapConnErr(pgcommon.RunInTx(ctx, pool, pgx.TxOptions{}, fn))
}

// wrapConnErr converts a connectivity failure (network unreachable, pool
// exhausted, TLS handshake, etc.) into domain.ErrCodeDependencyUnavailable
// so respondACLError surfaces it as 503, matching LLD §12.3/§20's
// documented behavior — previously dead code, since nothing in this
// package classified errors this way and a bare pgx/network error fell
// through respondACLError's default case to 500 instead. Errors that are
// already classified (a *domain.Error from IsUniqueViolation/optimistic-lock
// handling elsewhere in this package), a *pgconn.PgError (the server
// responded with a SQL error, not a connectivity failure), pgx.ErrNoRows,
// or a context cancellation/deadline (request-level, not a dependency
// outage) all pass through unchanged — mirrors iam-user-profile's
// identical wrapConnErr.
func wrapConnErr(err error) error {
	if err == nil {
		return nil
	}
	var de *domain.Error
	if errors.As(err, &de) {
		return err
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return domain.NewError(domain.ErrCodeDependencyUnavailable, "database unavailable: "+err.Error())
}

// TenderACLRepository implements port.TenderACLRepository against
// tender_acl_entries.
type TenderACLRepository struct {
	pool *pgcommon.Pool
}

var _ port.TenderACLRepository = (*TenderACLRepository)(nil)

// NewTenderACLRepository builds a TenderACLRepository.
func NewTenderACLRepository(pool *pgcommon.Pool) *TenderACLRepository {
	return &TenderACLRepository{pool: pool}
}

const selectColumnsSQL = `
SELECT id, tenant_id, tender_id, user_id, tenant_membership_id, access_level, granted_by,
       COALESCE(reason, ''), expires_at, record_version, created_at, updated_at, deleted_at
FROM tender_acl_entries`

// scanner is satisfied by both pgx.Row (QueryRow) and pgx.Rows (Query,
// inside a Next() loop).
type scanner interface {
	Scan(dest ...any) error
}

func scanEntry(row scanner) (domain.TenderACLEntry, error) {
	var e domain.TenderACLEntry
	var level string
	if err := row.Scan(
		&e.ID, &e.TenantID, &e.TenderID, &e.UserID, &e.TenantMembershipID, &level, &e.GrantedBy,
		&e.Reason, &e.ExpiresAt, &e.RecordVersion, &e.CreatedAt, &e.UpdatedAt, &e.DeletedAt,
	); err != nil {
		return domain.TenderACLEntry{}, err
	}
	e.AccessLevel = domain.TenderACLLevel(level)
	return e, nil
}

func collectEntries(rows pgx.Rows) ([]domain.TenderACLEntry, error) {
	result := []domain.TenderACLEntry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("scan tender_acl_entries row: %w", err)
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

// List implements TAC-1.
func (r *TenderACLRepository) List(ctx context.Context, tenantID, tenderID uuid.UUID) ([]domain.TenderACLEntry, error) {
	var result []domain.TenderACLEntry
	err := withTenant(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, selectColumnsSQL+`
			WHERE tenant_id = $1 AND tender_id = $2 AND deleted_at IS NULL
			ORDER BY created_at`, tenantID, tenderID)
		if err != nil {
			return fmt.Errorf("query tender_acl_entries: %w", err)
		}
		defer rows.Close()
		result, err = collectEntries(rows)
		return err
	})
	return result, err
}

// Grant implements TAC-2's write, mapping a uq_tae_active_entry violation
// to domain.ErrCodeDuplicateGrant.
func (r *TenderACLRepository) Grant(ctx context.Context, entry domain.TenderACLEntry) (domain.TenderACLEntry, error) {
	var created domain.TenderACLEntry
	err := withTenant(ctx, r.pool, entry.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		var reason any
		if entry.Reason != "" {
			reason = entry.Reason
		}

		row := tx.QueryRow(ctx, `
			INSERT INTO tender_acl_entries
				(tenant_id, tender_id, user_id, tenant_membership_id, access_level, granted_by, reason, expires_at)
			VALUES ($1, $2, $3, $4, $5::tender_acl_level, $6, $7, $8)
			RETURNING id, tenant_id, tender_id, user_id, tenant_membership_id, access_level, granted_by,
			          COALESCE(reason, ''), expires_at, record_version, created_at, updated_at, deleted_at`,
			entry.TenantID, entry.TenderID, entry.UserID, entry.TenantMembershipID,
			string(entry.AccessLevel), entry.GrantedBy, reason, entry.ExpiresAt,
		)

		e, err := scanEntry(row)
		if err != nil {
			if pgcommon.IsUniqueViolation(err) {
				return domain.NewError(domain.ErrCodeDuplicateGrant, "an active grant already exists for this tenant/tender/user")
			}
			return fmt.Errorf("insert tender_acl_entries: %w", err)
		}
		created = e
		return nil
	})
	return created, err
}

// Revoke implements TAC-3's write: soft-delete gated on expectedVersion
// matching the row's current record_version (LLD §11.2/§12.1). Zero rows
// affected — stale version, already revoked, or no such row — returns
// ErrCodeOptimisticLockConflict; the touch_row() trigger (migration 0002)
// bumps record_version on the UPDATE itself, so a caller that successfully
// revokes once and retries with the same version correctly conflicts
// rather than silently no-op'ing twice.
func (r *TenderACLRepository) Revoke(ctx context.Context, tenantID, tenderID, userID uuid.UUID, expectedVersion int64) error {
	return withTenant(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE tender_acl_entries SET deleted_at = now()
			WHERE tenant_id = $1 AND tender_id = $2 AND user_id = $3
			  AND deleted_at IS NULL AND record_version = $4`,
			tenantID, tenderID, userID, expectedVersion,
		)
		if err != nil {
			return fmt.Errorf("revoke tender_acl_entries: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return domain.NewError(domain.ErrCodeOptimisticLockConflict, "record version conflict")
		}
		return nil
	})
}

// FindActive implements the TAE-3 predicate for TAC-4/I-12.
func (r *TenderACLRepository) FindActive(ctx context.Context, tenantID, tenderID, userID uuid.UUID) (*domain.TenderACLEntry, error) {
	var result *domain.TenderACLEntry
	err := withTenant(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		row := tx.QueryRow(ctx, selectColumnsSQL+`
			WHERE tenant_id = $1 AND tender_id = $2 AND user_id = $3
			  AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at > now())`,
			tenantID, tenderID, userID,
		)
		e, err := scanEntry(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("query active tender_acl_entries: %w", err)
		}
		result = &e
		return nil
	})
	return result, err
}

// CascadeDeleteForTenant implements the tenant-offboarding cascade.
func (r *TenderACLRepository) CascadeDeleteForTenant(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	var deleted int64
	err := withTenant(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM tender_acl_entries WHERE tenant_id = $1`, tenantID)
		if err != nil {
			return fmt.Errorf("cascade delete tender_acl_entries: %w", err)
		}
		deleted = tag.RowsAffected()
		return nil
	})
	return deleted, err
}

// SoftDeleteForUser implements the per-user-removal ACL cascade (ADR-0007
// Wave 3 Phase 3). A soft delete, unlike CascadeDeleteForTenant — see
// port.TenderACLRepository's doc comment for the rationale.
func (r *TenderACLRepository) SoftDeleteForUser(ctx context.Context, tenantID, userID uuid.UUID) (int64, error) {
	var deleted int64
	err := withTenant(ctx, r.pool, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE tender_acl_entries SET deleted_at = now()
			WHERE tenant_id = $1 AND user_id = $2 AND deleted_at IS NULL`,
			tenantID, userID,
		)
		if err != nil {
			return fmt.Errorf("soft delete tender_acl_entries for user: %w", err)
		}
		deleted = tag.RowsAffected()
		return nil
	})
	return deleted, err
}
