// Package consumer implements this service's event-driven behavior:
// consuming TenantOffboarded from tenant-lifecycle-tenderacl-q (cascading
// the deletion of that tenant's tender_acl_entries rows) and
// TenantMembershipRemoved from member-removal-tenderacl-q (ADR-0007 Wave 3
// Phase 3, O_AND_M_DELTA.md §5 Option B — soft-deleting one removed user's
// rows, replacing the same-transaction SoftDeleteForUser call
// iam-org-membership's RemoveUser used to make before this table moved to
// its own database). This service publishes zero events (LLD §10.2/
// TAC-EVT-1) — there is no outbox, no SNS producer, and no glue
// registration anywhere in this codebase; these are inbound-only
// subscriptions.
package consumer

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ProcessedEvents implements the idempotency ledger backing both
// OffboardingConsumer and MemberRemovalConsumer. Not tenant-scoped, no RLS
// (LLD §7.3) — accessed only via the raw pool, never through
// pgcommon.WithTenantTx. One instance per consumer (the composite
// (event_id, consumer) primary key discriminates between them, so the two
// consumers safely share this one table without colliding on the same
// event_id from an unrelated relay).
type ProcessedEvents struct {
	pool     *pgxpool.Pool
	consumer string
}

// NewProcessedEvents builds a ProcessedEvents repository from the raw
// pgxpool.Pool underlying a pgcommon.Pool, scoped to the given consumer
// name (e.g. "tenant_lifecycle_cleanup", "member_removal").
func NewProcessedEvents(pool *pgxpool.Pool, consumer string) *ProcessedEvents {
	return &ProcessedEvents{pool: pool, consumer: consumer}
}

// IsProcessed reports whether eventID has already been recorded as
// successfully processed by this consumer.
func (r *ProcessedEvents) IsProcessed(ctx context.Context, eventID uuid.UUID) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM processed_events WHERE event_id = $1 AND consumer = $2)`,
		eventID, r.consumer,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check processed_events: %w", err)
	}
	return exists, nil
}

// MarkProcessed records eventID as successfully processed. Safe to call
// more than once for the same eventID.
func (r *ProcessedEvents) MarkProcessed(ctx context.Context, eventID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO processed_events (event_id, consumer, processed_at)
		VALUES ($1, $2, now())
		ON CONFLICT (event_id, consumer) DO NOTHING`,
		eventID, r.consumer,
	)
	if err != nil {
		return fmt.Errorf("mark processed_events: %w", err)
	}
	return nil
}

// CleanupExpired deletes this consumer's processed_events rows past the
// 8-day retention window (LLD §7.2.2/§18) and returns how many were
// removed. Scoped to r.consumer so cleaning up one consumer's ledger never
// touches the other's.
func (r *ProcessedEvents) CleanupExpired(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM processed_events WHERE consumer = $1 AND processed_at < now() - interval '8 days'`,
		r.consumer,
	)
	if err != nil {
		return 0, fmt.Errorf("cleanup processed_events: %w", err)
	}
	return tag.RowsAffected(), nil
}
