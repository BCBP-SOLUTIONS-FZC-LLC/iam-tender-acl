// Package consumer implements this service's event-driven behavior:
// consuming TenantMembershipsPurged (formerly TenantOffboarded — renamed by
// iam-org-membership under ADR-0008 to stop colliding with Realm
// Provisioner's own, differently-scoped TenantOffboarded event) from
// tenant-lifecycle-tenderacl-q (cascading the deletion of that tenant's
// tender_acl_entries rows) and MembershipRevoked (formerly
// TenantMembershipRemoved, consolidated by the same ADR-0008 pass) from
// member-removal-tenderacl-q (ADR-0007 Wave 3 Phase 3 — soft-deleting one
// removed user's rows, replacing the
// same-transaction SoftDeleteForUser call iam-org-membership's RemoveUser
// used to make before this table moved to its own database). This service
// publishes zero events (LLD §10.2/TAC-EVT-1) — there is no outbox, no SNS
// producer, and no glue registration anywhere in this codebase; these are
// inbound-only subscriptions. Idempotency uses Envelope.ID + INSERT ON
// CONFLICT DO NOTHING against processed_events (platform-events has no
// processed-events API); cascade and MarkProcessed share one TxRunner
// transaction (IDEMP-2).
package consumer

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/port"
	events "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-events/pkg/events"
)

// CascadeDeleter is the minimal slice of port.TenderACLRepository this
// consumer needs, kept local so this package doesn't couple to the rest of
// that port's interface — satisfied implicitly (Go structural typing) by
// *postgres.TenderACLRepository.
type CascadeDeleter interface {
	CascadeDeleteForTenant(ctx context.Context, tenantID uuid.UUID) (int64, error)
}

// IdempotencyStore is the minimal slice of postgres.ProcessedEvents this
// consumer needs. Kept local so this package does not import the outbound
// postgres adapter (arch-lint: inbound ↛ outbound).
type IdempotencyStore interface {
	IsProcessed(ctx context.Context, eventID uuid.UUID) (bool, error)
	MarkProcessed(ctx context.Context, eventID uuid.UUID) error
}

// CascadeMetrics is satisfied by *metrics.Metrics
// (internal/adapter/outbound/metrics), kept local so this package doesn't
// need to import that adapter package for the concrete type.
type CascadeMetrics interface {
	RecordCascade(ctx context.Context, result string)
	// RecordUnexpectedEventType makes the "ignoring unexpected event type"
	// WARN log below observable as a metric, not just a log line — this is
	// exactly the failure mode that let both cascades silently skip-and-ack
	// every delivery for a real stretch of time before the event-type
	// rename was caught (see CHANGELOG.md). Mirrors iam-org-membership's
	// iam_unknown_event_acknowledged_total.
	RecordUnexpectedEventType(ctx context.Context, queue, eventType string)
	RecordProcessedEventsDuplicate(ctx context.Context, consumer string)
}

// tenantMembershipsPurgedEventType was "TenantOffboarded" until
// iam-org-membership renamed its tenant-level cascade relay to
// "TenantMembershipsPurged" (ADR-0008) specifically so it would stop
// colliding with Realm Provisioner's own, differently-scoped
// TenantOffboarded event — Core never re-emits RP's event, it only
// consumes it. This queue's contract was always meant to be Core's own
// relay, not RP's raw event, so this is a straight rename, not a new
// producer.
const tenantMembershipsPurgedEventType = "TenantMembershipsPurged"

// tenantLifecycleQueueName is this consumer's queue, used only as the
// RecordUnexpectedEventType metric label — the SQS queue URL itself isn't
// available inside Handle.
const tenantLifecycleQueueName = "tenant-lifecycle-tenderacl-q"

// OffboardingConsumer handles tenant-lifecycle-tenderacl-q — this
// service's ONLY event-driven behavior (LLD §10.1/§11.4), replacing the
// lost fk_tae_tenant ON DELETE CASCADE (LLD §7.6.4). Its Handle method
// matches platform-events' events.Handler function type
// (func(ctx, events.Envelope[json.RawMessage]) error) and is passed
// directly to events.NewSQSConsumer in cmd/tender-acl/main.go.
type OffboardingConsumer struct {
	repo        CascadeDeleter
	idempotency IdempotencyStore
	tx          port.TxRunner
	metrics     CascadeMetrics
	logger      port.SlogStyleLogger
	tracer      trace.Tracer
}

// NewOffboardingConsumer builds an OffboardingConsumer.
func NewOffboardingConsumer(repo CascadeDeleter, idempotency IdempotencyStore, tx port.TxRunner, metrics CascadeMetrics, logger port.SlogStyleLogger, tracer trace.Tracer) *OffboardingConsumer {
	return &OffboardingConsumer{repo: repo, idempotency: idempotency, tx: tx, metrics: metrics, logger: logger, tracer: tracer}
}

// Handle implements the events.Handler function signature.
//
// Known types: skipDuplicate, then one TxRunner transaction for the
// cascade and MarkProcessed so a crash between them cannot leave the
// event unmarked after a committed delete (IDEMP-2). Unknown types:
// ackUnknown marks processed_events so redelivery does not storm.
func (c *OffboardingConsumer) Handle(ctx context.Context, env events.Envelope[json.RawMessage]) error {
	ctx, span := c.tracer.Start(ctx, "OffboardingConsumer.Handle")
	defer span.End()

	if env.Type != "" && env.Type != tenantMembershipsPurgedEventType {
		if err := ackUnknown(ctx, c.tx, c.idempotency, c.metrics, c.logger, tenantLifecycleQueueName, env); err != nil {
			return fmt.Errorf("offboardingconsumer: %w", err)
		}
		return nil
	}

	eventID, err := uuid.Parse(env.ID)
	if err != nil {
		return fmt.Errorf("offboardingconsumer: envelope missing/invalid event id %q: %w", env.ID, err)
	}
	tenantID, err := uuid.Parse(env.TenantID)
	if err != nil {
		return fmt.Errorf("offboardingconsumer: envelope missing/invalid tenant_id %q: %w", env.TenantID, err)
	}

	logger := c.logger.With(
		"tenant_id", tenantID.String(),
		"event_id", eventID.String(),
	)

	processed, err := skipDuplicate(ctx, c.idempotency, c.metrics, offboardingConsumerName, eventID)
	if err != nil {
		return fmt.Errorf("offboardingconsumer: check idempotency for event %s: %w", eventID, err)
	}
	if processed {
		logger.InfoContext(ctx, "duplicate TenantMembershipsPurged delivery, skipping cascade")
		return nil
	}

	gucCtx, err := withTenantGUC(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("offboardingconsumer: bind tenant GUC for %s: %w", tenantID, err)
	}
	return c.tx.RunInTx(gucCtx, func(txCtx context.Context) error {
		deleted, err := c.repo.CascadeDeleteForTenant(txCtx, tenantID)
		if err != nil {
			c.metrics.RecordCascade(txCtx, "error")
			return fmt.Errorf("offboardingconsumer: cascade delete for tenant %s: %w", tenantID, err)
		}
		if err := c.idempotency.MarkProcessed(txCtx, eventID); err != nil {
			return fmt.Errorf("offboardingconsumer: mark event %s processed: %w", eventID, err)
		}
		c.metrics.RecordCascade(txCtx, "success")
		logger.InfoContext(txCtx, "tenant offboarding cascade complete", "deleted", deleted)
		return nil
	})
}
