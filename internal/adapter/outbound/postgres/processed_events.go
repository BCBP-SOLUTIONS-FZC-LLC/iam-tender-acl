package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	pgcommon "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/pkg/pgcommon"
)

// ProcessedEvents implements the idempotency ledger backing both
// OffboardingConsumer and MemberRemovalConsumer. Not tenant-scoped, no RLS
// (LLD §7.3). Lives on the same pgcommon.Pool as TenderACLRepository so a
// cascade write and MarkProcessed can join one TxRunner transaction
// (withPool / IDEMP-2), matching iam-org-membership and
// iam-realm-provisioner. processed_events has no RLS, so an INSERT inside
// a GUC-bound tx is fine; CleanupExpired runs without a tenant GUC
// (GUCSetFromContext returns false → RunInTx skips GUC injection).
//
// One instance per consumer — the composite (event_id, consumer) primary
// key discriminates between them. platform-events has no processed-events
// API; Envelope.ID + INSERT ON CONFLICT DO NOTHING is the library's
// documented consumer pattern.
type ProcessedEvents struct {
	pool     *pgcommon.Pool
	consumer string
}

// NewProcessedEvents builds a ProcessedEvents repository from pool, scoped
// to the given consumer name (e.g. "tenant_lifecycle_cleanup",
// "member_removal").
func NewProcessedEvents(pool *pgcommon.Pool, consumer string) *ProcessedEvents {
	return &ProcessedEvents{pool: pool, consumer: consumer}
}

// IsProcessed reports whether eventID has already been recorded as
// successfully processed by this consumer.
func (r *ProcessedEvents) IsProcessed(ctx context.Context, eventID uuid.UUID) (bool, error) {
	var exists bool
	err := withPool(ctx, r.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM processed_events WHERE event_id = $1 AND consumer = $2)`,
			eventID, r.consumer,
		).Scan(&exists)
	})
	if err != nil {
		return false, fmt.Errorf("check processed_events: %w", err)
	}
	return exists, nil
}

// MarkProcessed records eventID as successfully processed. Inside
// TxRunner.RunInTx this joins the outer transaction so the dedup write
// commits or rolls back atomically with the cascade. Safe to call more
// than once for the same eventID (ON CONFLICT DO NOTHING).
func (r *ProcessedEvents) MarkProcessed(ctx context.Context, eventID uuid.UUID) error {
	err := withPool(ctx, r.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO processed_events (event_id, consumer, processed_at)
			VALUES ($1, $2, now())
			ON CONFLICT (event_id, consumer) DO NOTHING`,
			eventID, r.consumer,
		)
		return err
	})
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
	var deleted int64
	err := withPool(ctx, r.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`DELETE FROM processed_events WHERE consumer = $1 AND processed_at < now() - interval '8 days'`,
			r.consumer,
		)
		if err != nil {
			return err
		}
		deleted = tag.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("cleanup processed_events: %w", err)
	}
	return deleted, nil
}
