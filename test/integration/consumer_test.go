//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/adapter/inbound/consumer"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/domain"
	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/port"
	events "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-events/pkg/events"
)

func offboardedEnvelope(t *testing.T, eventID, tenantID uuid.UUID) events.Envelope[json.RawMessage] {
	t.Helper()
	return events.Envelope[json.RawMessage]{
		ID:        eventID.String(),
		Type:      "TenantMembershipsPurged",
		TenantID:  tenantID.String(),
		Timestamp: time.Now().UTC(),
	}
}

// TestOffboardingConsumer_Handle_CascadesAgainstRealPostgres exercises the
// full consumer -> repository -> real Postgres path: a tenant's rows are
// hard-deleted, and the event is recorded in processed_events.
func TestOffboardingConsumer_Handle_CascadesAgainstRealPostgres(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()
	tenantID := uuid.New()
	_, err := repo.Grant(ctx, domain.TenderACLEntry{
		TenantID: tenantID, TenderID: uuid.New(), UserID: uuid.New(),
		TenantMembershipID: uuid.New(), AccessLevel: domain.ACLView, GrantedBy: uuid.New(),
	})
	require.NoError(t, err)

	metrics := testMetrics(t)
	c := consumer.NewOffboardingConsumer(repo, processedEvents, txRunner, metrics, port.SlogStyleLogger{}, otel.Tracer("test"))

	eventID := uuid.New()
	require.NoError(t, c.Handle(ctx, offboardedEnvelope(t, eventID, tenantID)))

	var count int
	require.NoError(t, adminPool.QueryRow(ctx, `SELECT count(*) FROM tender_acl_entries WHERE tenant_id = $1`, tenantID).Scan(&count))
	assert.Equal(t, 0, count)

	processed, err := processedEvents.IsProcessed(ctx, eventID)
	require.NoError(t, err)
	assert.True(t, processed)
}

// TestOffboardingConsumer_Handle_DuplicateDelivery_CascadesOnce sends the
// same event twice (simulating SQS at-least-once redelivery) and asserts
// the cascade runs only once and processed_events records a single row.
func TestOffboardingConsumer_Handle_DuplicateDelivery_CascadesOnce(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()
	tenantID := uuid.New()
	_, err := repo.Grant(ctx, domain.TenderACLEntry{
		TenantID: tenantID, TenderID: uuid.New(), UserID: uuid.New(),
		TenantMembershipID: uuid.New(), AccessLevel: domain.ACLView, GrantedBy: uuid.New(),
	})
	require.NoError(t, err)

	metrics := testMetrics(t)
	c := consumer.NewOffboardingConsumer(repo, processedEvents, txRunner, metrics, port.SlogStyleLogger{}, otel.Tracer("test"))
	eventID := uuid.New()
	msg := offboardedEnvelope(t, eventID, tenantID)

	require.NoError(t, c.Handle(ctx, msg))
	// Second delivery of the exact same event_id — must be a no-op, not an
	// error, and must not attempt a second cascade.
	require.NoError(t, c.Handle(ctx, msg))

	var processedCount int
	require.NoError(t, adminPool.QueryRow(ctx,
		`SELECT count(*) FROM processed_events WHERE event_id = $1`, eventID.String(),
	).Scan(&processedCount))
	assert.Equal(t, 1, processedCount, "duplicate delivery must not create a second processed_events row")
}

func TestProcessedEvents_CleanupExpired_RemovesOldRowsOnly(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()
	recentID := uuid.New()
	require.NoError(t, processedEvents.MarkProcessed(ctx, recentID))

	oldID := uuid.New()
	_, err := adminPool.Exec(ctx,
		`INSERT INTO processed_events (event_id, consumer, processed_at) VALUES ($1, 'tenant_lifecycle_cleanup', now() - interval '9 days')`,
		oldID.String(),
	)
	require.NoError(t, err)

	deleted, err := processedEvents.CleanupExpired(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	stillProcessed, err := processedEvents.IsProcessed(ctx, recentID)
	require.NoError(t, err)
	assert.True(t, stillProcessed, "recent processed_events rows must survive an 8-day retention sweep")
}

// ── MemberRemovalConsumer (ADR-0007 Wave 3 Phase 3) ─────────────────────

func memberRemovedEnvelope(t *testing.T, eventID, tenantID, userID uuid.UUID) events.Envelope[json.RawMessage] {
	t.Helper()
	return events.Envelope[json.RawMessage]{
		ID:        eventID.String(),
		Type:      "MembershipRevoked",
		TenantID:  tenantID.String(),
		Subject:   userID.String(),
		Timestamp: time.Now().UTC(),
	}
}

// TestMemberRemovalConsumer_Handle_SoftDeletesAgainstRealPostgres exercises
// the full consumer -> repository -> real Postgres path: only the removed
// user's row is soft-deleted (row retained, deleted_at set — NOT a hard
// delete like the tenant-offboarding cascade), and the event is recorded
// in processed_events under the "member_removal" consumer name, distinct
// from "tenant_lifecycle_cleanup".
func TestMemberRemovalConsumer_Handle_SoftDeletesAgainstRealPostgres(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()
	tenantID, userA, userB := uuid.New(), uuid.New(), uuid.New()
	entryA, err := repo.Grant(ctx, domain.TenderACLEntry{
		TenantID: tenantID, TenderID: uuid.New(), UserID: userA,
		TenantMembershipID: uuid.New(), AccessLevel: domain.ACLEdit, GrantedBy: uuid.New(),
	})
	require.NoError(t, err)
	_, err = repo.Grant(ctx, domain.TenderACLEntry{
		TenantID: tenantID, TenderID: uuid.New(), UserID: userB,
		TenantMembershipID: uuid.New(), AccessLevel: domain.ACLView, GrantedBy: uuid.New(),
	})
	require.NoError(t, err)

	metrics := testMetrics(t)
	c := consumer.NewMemberRemovalConsumer(repo, memberRemovalProcessedEvents, txRunner, metrics, port.SlogStyleLogger{}, otel.Tracer("test"))

	eventID := uuid.New()
	require.NoError(t, c.Handle(ctx, memberRemovedEnvelope(t, eventID, tenantID, userA)))

	var aDeletedAt *time.Time
	require.NoError(t, adminPool.QueryRow(ctx,
		`SELECT deleted_at FROM tender_acl_entries WHERE id = $1`, entryA.ID,
	).Scan(&aDeletedAt))
	assert.NotNil(t, aDeletedAt, "member-removal cascade must soft-delete, not hard-delete")

	var bCount int
	require.NoError(t, adminPool.QueryRow(ctx,
		`SELECT count(*) FROM tender_acl_entries WHERE tenant_id = $1 AND user_id = $2 AND deleted_at IS NULL`,
		tenantID, userB,
	).Scan(&bCount))
	assert.Equal(t, 1, bCount, "member-removal cascade for userA must not touch userB's row")

	processed, err := memberRemovalProcessedEvents.IsProcessed(ctx, eventID)
	require.NoError(t, err)
	assert.True(t, processed)

	// The two consumers' idempotency ledgers are independent — this event
	// ID must not appear as processed under the other consumer name.
	var crossConsumerCount int
	require.NoError(t, adminPool.QueryRow(ctx,
		`SELECT count(*) FROM processed_events WHERE event_id = $1 AND consumer = 'tenant_lifecycle_cleanup'`,
		eventID.String(),
	).Scan(&crossConsumerCount))
	assert.Equal(t, 0, crossConsumerCount)
}

// TestMemberRemovalConsumer_Handle_DuplicateDelivery_CascadesOnce mirrors
// the offboarding consumer's equivalent test.
func TestMemberRemovalConsumer_Handle_DuplicateDelivery_CascadesOnce(t *testing.T) {
	cleanupTable(t)
	ctx := context.Background()
	tenantID, userID := uuid.New(), uuid.New()
	_, err := repo.Grant(ctx, domain.TenderACLEntry{
		TenantID: tenantID, TenderID: uuid.New(), UserID: userID,
		TenantMembershipID: uuid.New(), AccessLevel: domain.ACLView, GrantedBy: uuid.New(),
	})
	require.NoError(t, err)

	metrics := testMetrics(t)
	c := consumer.NewMemberRemovalConsumer(repo, memberRemovalProcessedEvents, txRunner, metrics, port.SlogStyleLogger{}, otel.Tracer("test"))
	eventID := uuid.New()
	msg := memberRemovedEnvelope(t, eventID, tenantID, userID)

	require.NoError(t, c.Handle(ctx, msg))
	require.NoError(t, c.Handle(ctx, msg))

	var processedCount int
	require.NoError(t, adminPool.QueryRow(ctx,
		`SELECT count(*) FROM processed_events WHERE event_id = $1 AND consumer = 'member_removal'`, eventID.String(),
	).Scan(&processedCount))
	assert.Equal(t, 1, processedCount, "duplicate delivery must not create a second processed_events row")
}
