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
