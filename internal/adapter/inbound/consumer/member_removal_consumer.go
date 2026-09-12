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

// UserRemovalCascader is the minimal slice of port.TenderACLRepository
// this consumer needs, kept local for the same reason as CascadeDeleter
// (offboarding_consumer.go) — satisfied implicitly by
// *postgres.TenderACLRepository.
type UserRemovalCascader interface {
	SoftDeleteForUser(ctx context.Context, tenantID, userID uuid.UUID) (int64, error)
}

// MemberRemovalMetrics is satisfied by *metrics.Metrics, kept local for
// the same reason as CascadeMetrics. A distinct metric/interface from
// CascadeMetrics deliberately — the tenant-offboarding and per-user-
// removal cascades are different failure domains (different queues,
// different DLQs) and must stay independently observable, not conflated
// into one series.
type MemberRemovalMetrics interface {
	RecordMemberRemovalCascade(ctx context.Context, result string)
	// RecordUnexpectedEventType — see CascadeMetrics' identical method doc
	// comment (offboarding_consumer.go); the exact same reasoning applies
	// here, since this consumer had the exact same silent-skip history.
	RecordUnexpectedEventType(ctx context.Context, queue, eventType string)
	RecordProcessedEventsDuplicate(ctx context.Context, consumer string)
}

// membershipRevokedEventType was "TenantMembershipRemoved" until
// iam-org-membership's ADR-0008 decomposition consolidated that
// tender-acl-specific event and the delegation-specific one it used to
// emit separately into a single shared "MembershipRevoked" event (see
// iam-org-membership's event_payloads.go doc comment on
// MembershipRevokedPayload). This consumer never received a
// MembershipRevoked delivery under the old name — it skip-and-acked every
// one — so the per-user ACL cascade below was silently never firing.
const membershipRevokedEventType = "MembershipRevoked"

// memberRemovalQueueName is this consumer's queue, used only as the
// RecordUnexpectedEventType metric label — the SQS queue URL itself isn't
// available inside Handle.
const memberRemovalQueueName = "member-removal-tenderacl-q"

// MemberRemovalConsumer handles member-removal-tenderacl-q, replacing the
// same-transaction SoftDeleteForUser call iam-org-membership's
// MembershipService.RemoveUser used to make before tender_acl_entries
// moved to its own database (LLD §10.1/§11.6, TAC-D10 — added after this
// document's v2.0 to cover the per-user-removal gap the original LLD
// didn't anticipate). Its Handle method matches platform-events'
// events.Handler function type, exactly like OffboardingConsumer.
type MemberRemovalConsumer struct {
	repo        UserRemovalCascader
	idempotency IdempotencyStore
	tx          port.TxRunner
	metrics     MemberRemovalMetrics
	logger      port.SlogStyleLogger
	tracer      trace.Tracer
}

// NewMemberRemovalConsumer builds a MemberRemovalConsumer.
func NewMemberRemovalConsumer(repo UserRemovalCascader, idempotency IdempotencyStore, tx port.TxRunner, metrics MemberRemovalMetrics, logger port.SlogStyleLogger, tracer trace.Tracer) *MemberRemovalConsumer {
	return &MemberRemovalConsumer{repo: repo, idempotency: idempotency, tx: tx, metrics: metrics, logger: logger, tracer: tracer}
}

// Handle implements the events.Handler function signature. Same
// idempotency discipline as OffboardingConsumer: skipDuplicate, then one
// TxRunner transaction for the soft-delete and MarkProcessed (IDEMP-2).
func (c *MemberRemovalConsumer) Handle(ctx context.Context, env events.Envelope[json.RawMessage]) error {
	ctx, span := c.tracer.Start(ctx, "MemberRemovalConsumer.Handle")
	defer span.End()

	if env.Type != "" && env.Type != membershipRevokedEventType {
		if err := ackUnknown(ctx, c.tx, c.idempotency, c.metrics, c.logger, memberRemovalQueueName, env); err != nil {
			return fmt.Errorf("memberremovalconsumer: %w", err)
		}
		return nil
	}

	eventID, err := uuid.Parse(env.ID)
	if err != nil {
		return fmt.Errorf("memberremovalconsumer: envelope missing/invalid event id %q: %w", env.ID, err)
	}
	tenantID, err := uuid.Parse(env.TenantID)
	if err != nil {
		return fmt.Errorf("memberremovalconsumer: envelope missing/invalid tenant_id %q: %w", env.TenantID, err)
	}
	// iam-org-membership's RemoveUser sets Subject to the removed user's
	// ID (see that repo's membership_service.go emit site) — the same
	// convention DelegationEnded/etc. already use for their own subject.
	userID, err := uuid.Parse(env.Subject)
	if err != nil {
		return fmt.Errorf("memberremovalconsumer: envelope missing/invalid subject (user_id) %q: %w", env.Subject, err)
	}

	logger := c.logger.With(
		"tenant_id", tenantID.String(),
		"user_id", userID.String(),
		"event_id", eventID.String(),
	)

	processed, err := skipDuplicate(ctx, c.idempotency, c.metrics, memberRemovalConsumerName, eventID)
	if err != nil {
		return fmt.Errorf("memberremovalconsumer: check idempotency for event %s: %w", eventID, err)
	}
	if processed {
		logger.InfoContext(ctx, "duplicate MembershipRevoked delivery, skipping cascade")
		return nil
	}

	gucCtx, err := withTenantGUC(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("memberremovalconsumer: bind tenant GUC for %s: %w", tenantID, err)
	}
	return c.tx.RunInTx(gucCtx, func(txCtx context.Context) error {
		deleted, err := c.repo.SoftDeleteForUser(txCtx, tenantID, userID)
		if err != nil {
			c.metrics.RecordMemberRemovalCascade(txCtx, "error")
			return fmt.Errorf("memberremovalconsumer: soft delete for user %s in tenant %s: %w", userID, tenantID, err)
		}
		if err := c.idempotency.MarkProcessed(txCtx, eventID); err != nil {
			return fmt.Errorf("memberremovalconsumer: mark event %s processed: %w", eventID, err)
		}
		c.metrics.RecordMemberRemovalCascade(txCtx, "success")
		logger.InfoContext(txCtx, "member removal ACL cascade complete", "deleted", deleted)
		return nil
	})
}
