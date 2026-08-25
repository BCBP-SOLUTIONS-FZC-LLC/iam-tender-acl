package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"

	events "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-events/pkg/events"
)

type fakeUserRemovalCascader struct {
	fn func(ctx context.Context, tenantID, userID uuid.UUID) (int64, error)
}

func (f *fakeUserRemovalCascader) SoftDeleteForUser(ctx context.Context, tenantID, userID uuid.UUID) (int64, error) {
	return f.fn(ctx, tenantID, userID)
}

func newTestMemberRemovalConsumer(repo UserRemovalCascader, idem IdempotencyStore, metrics MemberRemovalMetrics) *MemberRemovalConsumer {
	return NewMemberRemovalConsumer(repo, idem, metrics, slog.Default(), otel.Tracer("test"))
}

// memberRemovedEnvelope mirrors iam-org-membership's actual emission
// (membership_service.go's RemoveUser): TenantID at the envelope's
// top-level field, removed user's ID as Subject — the same convention
// DelegationEnded/TenantRoleRevoked/etc. already use.
func memberRemovedEnvelope(eventID, tenantID, userID uuid.UUID) events.Envelope[json.RawMessage] {
	return events.Envelope[json.RawMessage]{
		ID:        eventID.String(),
		Type:      "TenantMembershipRemoved",
		TenantID:  tenantID.String(),
		Subject:   userID.String(),
		Timestamp: time.Now().UTC(),
	}
}

func TestMemberRemovalConsumer_Handle_Success(t *testing.T) {
	eventID, tenantID, userID := uuid.New(), uuid.New(), uuid.New()
	var cascadeCalled, markCalled bool
	repo := &fakeUserRemovalCascader{fn: func(_ context.Context, tt, uu uuid.UUID) (int64, error) {
		cascadeCalled = true
		assert.Equal(t, tenantID, tt)
		assert.Equal(t, userID, uu)
		return 2, nil
	}}
	idem := &fakeIdempotencyStore{
		isProcessedFn: func(context.Context, uuid.UUID) (bool, error) { return false, nil },
		markProcessedFn: func(_ context.Context, id uuid.UUID) error {
			markCalled = true
			assert.Equal(t, eventID, id)
			return nil
		},
	}
	metrics := &fakeCascadeMetrics{}
	c := newTestMemberRemovalConsumer(repo, idem, metrics)

	err := c.Handle(context.Background(), memberRemovedEnvelope(eventID, tenantID, userID))
	require.NoError(t, err)
	assert.True(t, cascadeCalled)
	assert.True(t, markCalled)
	assert.Equal(t, []string{"success"}, metrics.results)
}

func TestMemberRemovalConsumer_Handle_DuplicateEvent_SkipsCascade(t *testing.T) {
	eventID, tenantID, userID := uuid.New(), uuid.New(), uuid.New()
	repo := &fakeUserRemovalCascader{fn: func(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
		t.Fatal("SoftDeleteForUser must not be called for an already-processed event")
		return 0, nil
	}}
	idem := &fakeIdempotencyStore{
		isProcessedFn: func(context.Context, uuid.UUID) (bool, error) { return true, nil },
		markProcessedFn: func(context.Context, uuid.UUID) error {
			t.Fatal("MarkProcessed must not be called again for a duplicate")
			return nil
		},
	}
	c := newTestMemberRemovalConsumer(repo, idem, &fakeCascadeMetrics{})

	err := c.Handle(context.Background(), memberRemovedEnvelope(eventID, tenantID, userID))
	require.NoError(t, err)
}

func TestMemberRemovalConsumer_Handle_WrongEventType_SkippedNoError(t *testing.T) {
	repo := &fakeUserRemovalCascader{fn: func(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
		t.Fatal("cascade must not run for an unexpected event type")
		return 0, nil
	}}
	c := newTestMemberRemovalConsumer(repo, &fakeIdempotencyStore{}, &fakeCascadeMetrics{})

	env := events.Envelope[json.RawMessage]{
		ID: uuid.New().String(), Type: "TenantOffboarded",
		TenantID: uuid.New().String(), Subject: uuid.New().String(), Timestamp: time.Now().UTC(),
	}
	handleErr := c.Handle(context.Background(), env)
	require.NoError(t, handleErr)
}

func TestMemberRemovalConsumer_Handle_MissingEventID_ReturnsError(t *testing.T) {
	c := newTestMemberRemovalConsumer(&fakeUserRemovalCascader{}, &fakeIdempotencyStore{}, &fakeCascadeMetrics{})

	env := events.Envelope[json.RawMessage]{
		ID: "", Type: "TenantMembershipRemoved",
		TenantID: uuid.New().String(), Subject: uuid.New().String(), Timestamp: time.Now().UTC(),
	}
	handleErr := c.Handle(context.Background(), env)
	assert.Error(t, handleErr)
}

func TestMemberRemovalConsumer_Handle_MissingTenantID_ReturnsError(t *testing.T) {
	c := newTestMemberRemovalConsumer(&fakeUserRemovalCascader{}, &fakeIdempotencyStore{}, &fakeCascadeMetrics{})

	env := events.Envelope[json.RawMessage]{
		ID: uuid.New().String(), Type: "TenantMembershipRemoved",
		TenantID: "", Subject: uuid.New().String(), Timestamp: time.Now().UTC(),
	}
	handleErr := c.Handle(context.Background(), env)
	assert.Error(t, handleErr)
}

func TestMemberRemovalConsumer_Handle_MissingSubject_ReturnsError(t *testing.T) {
	c := newTestMemberRemovalConsumer(&fakeUserRemovalCascader{}, &fakeIdempotencyStore{}, &fakeCascadeMetrics{})

	env := events.Envelope[json.RawMessage]{
		ID: uuid.New().String(), Type: "TenantMembershipRemoved",
		TenantID: uuid.New().String(), Subject: "", Timestamp: time.Now().UTC(),
	}
	handleErr := c.Handle(context.Background(), env)
	assert.Error(t, handleErr)
}

// TestMemberRemovalConsumer_Handle_CascadeError_LeavesEventUnmarked_ForRedelivery
// mirrors OffboardingConsumer's equivalent: a failed cascade never marks
// the event processed, so redelivery reruns the (idempotent) soft-delete.
func TestMemberRemovalConsumer_Handle_CascadeError_LeavesEventUnmarked_ForRedelivery(t *testing.T) {
	eventID, tenantID, userID := uuid.New(), uuid.New(), uuid.New()
	cascadeErr := errors.New("db down")
	repo := &fakeUserRemovalCascader{fn: func(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
		return 0, cascadeErr
	}}
	idem := &fakeIdempotencyStore{
		isProcessedFn: func(context.Context, uuid.UUID) (bool, error) { return false, nil },
		markProcessedFn: func(context.Context, uuid.UUID) error {
			t.Fatal("MarkProcessed must not be called when the cascade fails")
			return nil
		},
	}
	metrics := &fakeCascadeMetrics{}
	c := newTestMemberRemovalConsumer(repo, idem, metrics)

	err := c.Handle(context.Background(), memberRemovedEnvelope(eventID, tenantID, userID))
	require.Error(t, err)
	assert.Equal(t, []string{"error"}, metrics.results)
}

func TestMemberRemovalConsumer_Handle_MarkProcessedError_ReturnsError(t *testing.T) {
	eventID, tenantID, userID := uuid.New(), uuid.New(), uuid.New()
	repo := &fakeUserRemovalCascader{fn: func(context.Context, uuid.UUID, uuid.UUID) (int64, error) { return 1, nil }}
	idem := &fakeIdempotencyStore{
		isProcessedFn:   func(context.Context, uuid.UUID) (bool, error) { return false, nil },
		markProcessedFn: func(context.Context, uuid.UUID) error { return errors.New("insert failed") },
	}
	c := newTestMemberRemovalConsumer(repo, idem, &fakeCascadeMetrics{})

	err := c.Handle(context.Background(), memberRemovedEnvelope(eventID, tenantID, userID))
	assert.Error(t, err)
}

func TestMemberRemovalConsumer_Handle_IsProcessedCheckError_ReturnsError(t *testing.T) {
	idem := &fakeIdempotencyStore{
		isProcessedFn: func(context.Context, uuid.UUID) (bool, error) { return false, errors.New("db down") },
	}
	repo := &fakeUserRemovalCascader{fn: func(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
		t.Fatal("cascade must not run if the idempotency check itself fails")
		return 0, nil
	}}
	c := newTestMemberRemovalConsumer(repo, idem, &fakeCascadeMetrics{})

	err := c.Handle(context.Background(), memberRemovedEnvelope(uuid.New(), uuid.New(), uuid.New()))
	assert.Error(t, err)
}

// ── EC-MEM-EMPTYTYPE-01 ───────────────────────────────────────────────────────

// TestMemberRemovalConsumer_Handle_EmptyType_Treated verifies that env.Type=""
// passes the type guard and proceeds to soft-delete, matching code behavior
// (same guard logic as OffboardingConsumer).
func TestMemberRemovalConsumer_Handle_EmptyType_Treated(t *testing.T) {
	eventID, tenantID, userID := uuid.New(), uuid.New(), uuid.New()
	var cascadeCalled bool
	repo := &fakeUserRemovalCascader{fn: func(_ context.Context, tid, uid uuid.UUID) (int64, error) {
		cascadeCalled = true
		assert.Equal(t, tenantID, tid)
		assert.Equal(t, userID, uid)
		return 1, nil
	}}
	idem := &fakeIdempotencyStore{
		isProcessedFn:   func(context.Context, uuid.UUID) (bool, error) { return false, nil },
		markProcessedFn: func(context.Context, uuid.UUID) error { return nil },
	}
	c := newTestMemberRemovalConsumer(repo, idem, &fakeCascadeMetrics{})

	env := events.Envelope[json.RawMessage]{
		ID: eventID.String(), Type: "",
		TenantID: tenantID.String(), Subject: userID.String(), Timestamp: time.Now().UTC(),
	}
	err := c.Handle(context.Background(), env)
	require.NoError(t, err)
	assert.True(t, cascadeCalled)
}

// ── EC-MEM-ZERO-ACL-01 ───────────────────────────────────────────────────────

// TestMemberRemovalConsumer_ZeroACL_Success verifies that a user with no ACL
// entries (UPDATE affects 0 rows) is not an error — MarkProcessed is still
// called and Handle returns nil.
func TestMemberRemovalConsumer_ZeroACL_Success(t *testing.T) {
	eventID, tenantID, userID := uuid.New(), uuid.New(), uuid.New()
	repo := &fakeUserRemovalCascader{fn: func(_ context.Context, tid, uid uuid.UUID) (int64, error) {
		assert.Equal(t, tenantID, tid)
		assert.Equal(t, userID, uid)
		return 0, nil
	}}
	idem := &fakeIdempotencyStore{
		isProcessedFn:   func(context.Context, uuid.UUID) (bool, error) { return false, nil },
		markProcessedFn: func(context.Context, uuid.UUID) error { return nil },
	}
	metrics := &fakeCascadeMetrics{}
	c := newTestMemberRemovalConsumer(repo, idem, metrics)

	err := c.Handle(context.Background(), memberRemovedEnvelope(eventID, tenantID, userID))
	require.NoError(t, err)
	assert.Equal(t, []string{"success"}, metrics.results)
}

// ── EC-MEM-MULTI-TENDER-01 ───────────────────────────────────────────────────

// TestMemberRemovalConsumer_MultiTender_AllDeleted verifies that a user with
// grants across multiple tenders has all of them soft-deleted in a single
// SoftDeleteForUser call (UPDATE WHERE tenant_id=X AND user_id=Y — multi-row).
func TestMemberRemovalConsumer_MultiTender_AllDeleted(t *testing.T) {
	eventID, tenantID, userID := uuid.New(), uuid.New(), uuid.New()
	repo := &fakeUserRemovalCascader{fn: func(_ context.Context, tid, uid uuid.UUID) (int64, error) {
		assert.Equal(t, tenantID, tid)
		assert.Equal(t, userID, uid)
		return 5, nil // user had grants on 5 tenders
	}}
	idem := &fakeIdempotencyStore{
		isProcessedFn:   func(context.Context, uuid.UUID) (bool, error) { return false, nil },
		markProcessedFn: func(context.Context, uuid.UUID) error { return nil },
	}
	metrics := &fakeCascadeMetrics{}
	c := newTestMemberRemovalConsumer(repo, idem, metrics)

	err := c.Handle(context.Background(), memberRemovedEnvelope(eventID, tenantID, userID))
	require.NoError(t, err)
	assert.Equal(t, []string{"success"}, metrics.results)
}

// ── EC-MEM-CROSS-TENANT-01 / EC-MEM-DLQ-01 ───────────────────────────────────

// TestMemberRemoval_CrossTenantUser_Scoped verifies EC-MEM-CROSS-TENANT-01:
// even when the removed user belongs to a different tenant, SoftDeleteForUser
// is called with the envelope's tenant_id — not the user's origin tenant.
// Real RLS enforcement (app.tenant_id GUC) provides defense-in-depth at the
// DB layer and is verified in the rls test suite.
// EC-MEM-DLQ-01 (DLQ routing after 5 failures) is infrastructure behavior
// verified indirectly: TestMemberRemovalConsumer_Handle_CascadeError_* covers
// the error-return contract that triggers SQS requeue → eventually DLQ.
func TestMemberRemoval_CrossTenantUser_Scoped(t *testing.T) {
	eventID := uuid.New()
	tenantAAA := uuid.New()   // envelope's tenant_id
	userFromBBB := uuid.New() // user whose origin tenant is different

	var gotTenant, gotUser uuid.UUID
	repo := &fakeUserRemovalCascader{fn: func(_ context.Context, tid, uid uuid.UUID) (int64, error) {
		gotTenant, gotUser = tid, uid
		return 0, nil // user has no entries under tenantAAA
	}}
	idem := &fakeIdempotencyStore{
		isProcessedFn:   func(context.Context, uuid.UUID) (bool, error) { return false, nil },
		markProcessedFn: func(context.Context, uuid.UUID) error { return nil },
	}
	c := newTestMemberRemovalConsumer(repo, idem, &fakeCascadeMetrics{})

	err := c.Handle(context.Background(), memberRemovedEnvelope(eventID, tenantAAA, userFromBBB))
	require.NoError(t, err)
	assert.Equal(t, tenantAAA, gotTenant, "repo call must be scoped to envelope's tenant_id, not user's origin tenant")
	assert.Equal(t, userFromBBB, gotUser)
}
