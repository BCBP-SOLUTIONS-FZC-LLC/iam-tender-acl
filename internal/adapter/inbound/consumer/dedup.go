package consumer

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/port"
	events "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-events/pkg/events"
	pgdomain "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/pkg/domain"
	pgcommon "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/pkg/pgcommon"
)

const (
	offboardingConsumerName   = "tenant_lifecycle_cleanup"
	memberRemovalConsumerName = "member_removal"
)

// skipDuplicate is the cheap processed_events probe every known-type
// handler runs before opening a write transaction. A hit increments
// processed_events_duplicates_total (IDEMP-4) and short-circuits.
// platform-events has no processed-events API — Envelope.ID + INSERT
// ON CONFLICT DO NOTHING is the library's documented consumer pattern
// (same as iam-org-membership / iam-realm-provisioner).
func skipDuplicate(ctx context.Context, dedup IdempotencyStore, metrics DuplicateMetrics, consumer string, eventID uuid.UUID) (bool, error) {
	processed, err := dedup.IsProcessed(ctx, eventID)
	if err != nil {
		return false, err
	}
	if processed {
		metrics.RecordProcessedEventsDuplicate(ctx, consumer)
		return true, nil
	}
	return false, nil
}

// unexpectedMetrics is the slice of CascadeMetrics / MemberRemovalMetrics
// ackUnknown needs.
type unexpectedMetrics interface {
	RecordUnexpectedEventType(ctx context.Context, queue, eventType string)
}

// DuplicateMetrics is the slice skipDuplicate needs, satisfied by
// *metrics.Metrics.
type DuplicateMetrics interface {
	RecordProcessedEventsDuplicate(ctx context.Context, consumer string)
}

// ackUnknown is the forward-compat path: an event type with no wired
// handler is logged, counted, and recorded in processed_events inside a
// RunInTx so redelivery does not storm the same unknown type. If env.ID
// is not a UUID this table's event_id column cannot store it — the
// message is still acked without a mark so a poison ID does not retry
// forever.
func ackUnknown(ctx context.Context, tx port.TxRunner, dedup IdempotencyStore, metrics unexpectedMetrics, log port.SlogStyleLogger, queue string, env events.Envelope[json.RawMessage]) error {
	metrics.RecordUnexpectedEventType(ctx, queue, env.Type)
	log.WarnContext(ctx, "unknown event type — silently acknowledging",
		"queue", queue,
		"event_type", env.Type,
		"event_id", env.ID,
	)
	eventID, err := uuid.Parse(env.ID)
	if err != nil {
		return nil
	}
	return markProcessedInTx(ctx, tx, dedup, eventID)
}

// markProcessedInTx records eventID on the caller's TxRunner so the dedup
// insert joins withPool's ambient transaction (or opens its own when the
// handler has no local write to be atomic with).
func markProcessedInTx(ctx context.Context, tx port.TxRunner, dedup IdempotencyStore, eventID uuid.UUID) error {
	return tx.RunInTx(ctx, func(txCtx context.Context) error {
		if err := dedup.MarkProcessed(txCtx, eventID); err != nil {
			return fmt.Errorf("mark processed: %w", err)
		}
		return nil
	})
}

// withTenantGUC binds app.tenant_id into ctx via pgcommon's GUCSet so the
// next TxRunner.RunInTx issues SET LOCAL app.tenant_id. TenantID only —
// this service's RLS policy never filters on user_id, matching withTenant.
func withTenantGUC(ctx context.Context, tenantID uuid.UUID) (context.Context, error) {
	return pgcommon.WithValidatedGUCSet(ctx, pgdomain.GUCSet{TenantID: tenantID.String()})
}
