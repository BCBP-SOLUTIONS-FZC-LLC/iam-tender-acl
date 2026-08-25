package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"

	events "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-events/pkg/events"
)

// CascadeDeleter is the minimal slice of port.TenderACLRepository this
// consumer needs, kept local so this package doesn't couple to the rest of
// that port's interface — satisfied implicitly (Go structural typing) by
// *postgres.TenderACLRepository.
type CascadeDeleter interface {
	CascadeDeleteForTenant(ctx context.Context, tenantID uuid.UUID) (int64, error)
}

// IdempotencyStore is the minimal slice of ProcessedEvents this consumer
// needs.
type IdempotencyStore interface {
	IsProcessed(ctx context.Context, eventID uuid.UUID) (bool, error)
	MarkProcessed(ctx context.Context, eventID uuid.UUID) error
}

// CascadeMetrics is satisfied by *metrics.Metrics
// (internal/adapter/outbound/metrics), kept local so this package doesn't
// need to import that adapter package for the concrete type.
type CascadeMetrics interface {
	RecordCascade(ctx context.Context, result string)
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

// OffboardingConsumer handles tenant-lifecycle-tenderacl-q — this
// service's ONLY event-driven behavior (LLD §10.1/§11.4), replacing the
// lost fk_tae_tenant ON DELETE CASCADE (LLD §7.6.4). Its Handle method
// matches platform-events' events.Handler function type
// (func(ctx, events.Envelope[json.RawMessage]) error) and is passed
// directly to events.NewSQSConsumer in cmd/tender-acl/main.go.
type OffboardingConsumer struct {
	repo        CascadeDeleter
	idempotency IdempotencyStore
	metrics     CascadeMetrics
	logger      *slog.Logger
	tracer      trace.Tracer
}

// NewOffboardingConsumer builds an OffboardingConsumer.
func NewOffboardingConsumer(repo CascadeDeleter, idempotency IdempotencyStore, metrics CascadeMetrics, logger *slog.Logger, tracer trace.Tracer) *OffboardingConsumer {
	return &OffboardingConsumer{repo: repo, idempotency: idempotency, metrics: metrics, logger: logger, tracer: tracer}
}

// Handle implements the events.Handler function signature.
//
// Idempotency is checked before doing any work and recorded only after the
// cascade fully succeeds, so a crash mid-cascade simply leaves the event
// unmarked and redelivery reruns the (idempotent) cascade to completion.
// Returning a non-nil error leaves the message on the queue for redelivery.
func (c *OffboardingConsumer) Handle(ctx context.Context, env events.Envelope[json.RawMessage]) error {
	ctx, span := c.tracer.Start(ctx, "OffboardingConsumer.Handle")
	defer span.End()

	if env.Type != "" && env.Type != tenantMembershipsPurgedEventType {
		c.logger.WarnContext(ctx, "ignoring unexpected event type on tenant-lifecycle-tenderacl-q",
			slog.String("event_type", env.Type),
			slog.String("event_id", env.ID),
		)
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
		slog.String("tenant_id", tenantID.String()),
		slog.String("event_id", eventID.String()),
	)

	processed, err := c.idempotency.IsProcessed(ctx, eventID)
	if err != nil {
		return fmt.Errorf("offboardingconsumer: check idempotency for event %s: %w", eventID, err)
	}
	if processed {
		logger.InfoContext(ctx, "duplicate TenantMembershipsPurged delivery, skipping cascade")
		return nil
	}

	deleted, err := c.repo.CascadeDeleteForTenant(ctx, tenantID)
	if err != nil {
		c.metrics.RecordCascade(ctx, "error")
		return fmt.Errorf("offboardingconsumer: cascade delete for tenant %s: %w", tenantID, err)
	}

	if err := c.idempotency.MarkProcessed(ctx, eventID); err != nil {
		return fmt.Errorf("offboardingconsumer: mark event %s processed: %w", eventID, err)
	}

	c.metrics.RecordCascade(ctx, "success")
	logger.InfoContext(ctx, "tenant offboarding cascade complete", slog.Int64("deleted", deleted))
	return nil
}
