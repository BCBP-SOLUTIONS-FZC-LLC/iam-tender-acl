package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"

	events "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-events/pkg/events"
)

type fakeCascadeDeleter struct {
	fn func(ctx context.Context, tenantID uuid.UUID) (int64, error)
}

func (f *fakeCascadeDeleter) CascadeDeleteForTenant(ctx context.Context, tenantID uuid.UUID) (int64, error) {
	return f.fn(ctx, tenantID)
}

type fakeIdempotencyStore struct {
	isProcessedFn   func(ctx context.Context, eventID uuid.UUID) (bool, error)
	markProcessedFn func(ctx context.Context, eventID uuid.UUID) error
}

func (f *fakeIdempotencyStore) IsProcessed(ctx context.Context, eventID uuid.UUID) (bool, error) {
	return f.isProcessedFn(ctx, eventID)
}
func (f *fakeIdempotencyStore) MarkProcessed(ctx context.Context, eventID uuid.UUID) error {
	return f.markProcessedFn(ctx, eventID)
}

type fakeCascadeMetrics struct{ results []string }

func (f *fakeCascadeMetrics) RecordCascade(_ context.Context, result string) {
	f.results = append(f.results, result)
}

// RecordMemberRemovalCascade lets this same fake double as a
// MemberRemovalMetrics in member_removal_consumer_test.go — both record
// into the same results slice since no test needs to distinguish which
// method was called, only the result values.
func (f *fakeCascadeMetrics) RecordMemberRemovalCascade(_ context.Context, result string) {
	f.results = append(f.results, result)
}

func newTestConsumer(repo CascadeDeleter, idem IdempotencyStore, metrics CascadeMetrics) *OffboardingConsumer {
	return NewOffboardingConsumer(repo, idem, metrics, slog.Default(), otel.Tracer("test"))
}

func offboardedEnvelope(eventID, tenantID uuid.UUID) events.Envelope[json.RawMessage] {
	return events.Envelope[json.RawMessage]{
		ID:        eventID.String(),
		Type:      "TenantOffboarded",
		TenantID:  tenantID.String(),
		Timestamp: time.Now().UTC(),
	}
}

func TestConsumer_Handle_Success(t *testing.T) {
	eventID, tenantID := uuid.New(), uuid.New()
	var cascadeCalled, markCalled bool
	repo := &fakeCascadeDeleter{fn: func(_ context.Context, tt uuid.UUID) (int64, error) {
		cascadeCalled = true
		assert.Equal(t, tenantID, tt)
		return 3, nil
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
	c := newTestConsumer(repo, idem, metrics)

	err := c.Handle(context.Background(), offboardedEnvelope(eventID, tenantID))
	require.NoError(t, err)
	assert.True(t, cascadeCalled)
	assert.True(t, markCalled)
	assert.Equal(t, []string{"success"}, metrics.results)
}

func TestConsumer_Handle_DuplicateEvent_SkipsCascade(t *testing.T) {
	eventID, tenantID := uuid.New(), uuid.New()
	repo := &fakeCascadeDeleter{fn: func(context.Context, uuid.UUID) (int64, error) {
		t.Fatal("CascadeDeleteForTenant must not be called for an already-processed event")
		return 0, nil
	}}
	idem := &fakeIdempotencyStore{
		isProcessedFn: func(context.Context, uuid.UUID) (bool, error) { return true, nil },
		markProcessedFn: func(context.Context, uuid.UUID) error {
			t.Fatal("MarkProcessed must not be called again for a duplicate")
			return nil
		},
	}
	c := newTestConsumer(repo, idem, &fakeCascadeMetrics{})

	err := c.Handle(context.Background(), offboardedEnvelope(eventID, tenantID))
	require.NoError(t, err)
}

func TestConsumer_Handle_WrongEventType_SkippedNoError(t *testing.T) {
	repo := &fakeCascadeDeleter{fn: func(context.Context, uuid.UUID) (int64, error) {
		t.Fatal("cascade must not run for an unexpected event type")
		return 0, nil
	}}
	c := newTestConsumer(repo, &fakeIdempotencyStore{}, &fakeCascadeMetrics{})

	env := events.Envelope[json.RawMessage]{ID: uuid.New().String(), Type: "SomeOtherEvent", TenantID: uuid.New().String(), Timestamp: time.Now().UTC()}
	handleErr := c.Handle(context.Background(), env)
	require.NoError(t, handleErr)
}

func TestConsumer_Handle_MissingEventID_ReturnsError(t *testing.T) {
	c := newTestConsumer(&fakeCascadeDeleter{}, &fakeIdempotencyStore{}, &fakeCascadeMetrics{})

	env := events.Envelope[json.RawMessage]{ID: "", Type: "TenantOffboarded", TenantID: uuid.New().String(), Timestamp: time.Now().UTC()}
	handleErr := c.Handle(context.Background(), env)
	assert.Error(t, handleErr)
}

func TestConsumer_Handle_MissingTenantID_ReturnsError(t *testing.T) {
	c := newTestConsumer(&fakeCascadeDeleter{}, &fakeIdempotencyStore{}, &fakeCascadeMetrics{})

	env := events.Envelope[json.RawMessage]{ID: uuid.New().String(), Type: "TenantOffboarded", TenantID: "", Timestamp: time.Now().UTC()}
	handleErr := c.Handle(context.Background(), env)
	assert.Error(t, handleErr)
}

// TestConsumer_Handle_CascadeError_LeavesEventUnmarked_ForRedelivery
// confirms a non-nil error from the cascade both surfaces (leaving the
// message on the queue) AND never marks the event processed, so a retry
// reruns the (idempotent) cascade to completion.
func TestConsumer_Handle_CascadeError_LeavesEventUnmarked_ForRedelivery(t *testing.T) {
	eventID, tenantID := uuid.New(), uuid.New()
	cascadeErr := errors.New("db down")
	repo := &fakeCascadeDeleter{fn: func(context.Context, uuid.UUID) (int64, error) {
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
	c := newTestConsumer(repo, idem, metrics)

	err := c.Handle(context.Background(), offboardedEnvelope(eventID, tenantID))
	require.Error(t, err)
	assert.Equal(t, []string{"error"}, metrics.results)
}

func TestConsumer_Handle_MarkProcessedError_ReturnsError(t *testing.T) {
	eventID, tenantID := uuid.New(), uuid.New()
	repo := &fakeCascadeDeleter{fn: func(context.Context, uuid.UUID) (int64, error) { return 1, nil }}
	idem := &fakeIdempotencyStore{
		isProcessedFn:   func(context.Context, uuid.UUID) (bool, error) { return false, nil },
		markProcessedFn: func(context.Context, uuid.UUID) error { return errors.New("insert failed") },
	}
	c := newTestConsumer(repo, idem, &fakeCascadeMetrics{})

	err := c.Handle(context.Background(), offboardedEnvelope(eventID, tenantID))
	assert.Error(t, err)
}

func TestConsumer_Handle_IsProcessedCheckError_ReturnsError(t *testing.T) {
	idem := &fakeIdempotencyStore{
		isProcessedFn: func(context.Context, uuid.UUID) (bool, error) { return false, errors.New("db down") },
	}
	repo := &fakeCascadeDeleter{fn: func(context.Context, uuid.UUID) (int64, error) {
		t.Fatal("cascade must not run if the idempotency check itself fails")
		return 0, nil
	}}
	c := newTestConsumer(repo, idem, &fakeCascadeMetrics{})

	err := c.Handle(context.Background(), offboardedEnvelope(uuid.New(), uuid.New()))
	assert.Error(t, err)
}

// ── EC-OFF-EMPTYTYPE-01 ───────────────────────────────────────────────────────

// TestConsumer_Handle_EmptyType_TreatedAsOffboarded verifies that an envelope
// with env.Type="" passes the type guard
// (`env.Type != "" && env.Type != "TenantOffboarded"` → first condition false
// → whole guard false) and proceeds to cascade, matching code behavior.
func TestConsumer_Handle_EmptyType_TreatedAsOffboarded(t *testing.T) {
	eventID, tenantID := uuid.New(), uuid.New()
	var cascadeCalled bool
	repo := &fakeCascadeDeleter{fn: func(_ context.Context, tid uuid.UUID) (int64, error) {
		cascadeCalled = true
		assert.Equal(t, tenantID, tid)
		return 2, nil
	}}
	idem := &fakeIdempotencyStore{
		isProcessedFn:   func(context.Context, uuid.UUID) (bool, error) { return false, nil },
		markProcessedFn: func(context.Context, uuid.UUID) error { return nil },
	}
	c := newTestConsumer(repo, idem, &fakeCascadeMetrics{})

	env := events.Envelope[json.RawMessage]{
		ID: eventID.String(), Type: "",
		TenantID: tenantID.String(), Timestamp: time.Now().UTC(),
	}
	err := c.Handle(context.Background(), env)
	require.NoError(t, err)
	assert.True(t, cascadeCalled)
}

// ── EC-OFF-ZERO-ACL-01 ───────────────────────────────────────────────────────

// TestOffboardingConsumer_ZeroACL_Success verifies that a tenant with zero ACL
// entries (DELETE affects 0 rows) is not an error — MarkProcessed is still
// called and Handle returns nil.
func TestOffboardingConsumer_ZeroACL_Success(t *testing.T) {
	eventID, tenantID := uuid.New(), uuid.New()
	repo := &fakeCascadeDeleter{fn: func(_ context.Context, tid uuid.UUID) (int64, error) {
		assert.Equal(t, tenantID, tid)
		return 0, nil
	}}
	idem := &fakeIdempotencyStore{
		isProcessedFn:   func(context.Context, uuid.UUID) (bool, error) { return false, nil },
		markProcessedFn: func(context.Context, uuid.UUID) error { return nil },
	}
	metrics := &fakeCascadeMetrics{}
	c := newTestConsumer(repo, idem, metrics)

	err := c.Handle(context.Background(), offboardedEnvelope(eventID, tenantID))
	require.NoError(t, err)
	assert.Equal(t, []string{"success"}, metrics.results)
}

// ── EC-OFF-LARGE-01 ──────────────────────────────────────────────────────────

// TestOffboardingConsumer_LargeCascade verifies that a tenant with a very large
// number of ACL entries (100,000+) completes successfully. The single-statement
// DELETE is handled by Postgres; the mock returns the row count immediately.
func TestOffboardingConsumer_LargeCascade(t *testing.T) {
	const rowCount = int64(100_000)
	eventID, tenantID := uuid.New(), uuid.New()
	repo := &fakeCascadeDeleter{fn: func(_ context.Context, tid uuid.UUID) (int64, error) {
		assert.Equal(t, tenantID, tid)
		return rowCount, nil
	}}
	idem := &fakeIdempotencyStore{
		isProcessedFn:   func(context.Context, uuid.UUID) (bool, error) { return false, nil },
		markProcessedFn: func(context.Context, uuid.UUID) error { return nil },
	}
	metrics := &fakeCascadeMetrics{}
	c := newTestConsumer(repo, idem, metrics)

	err := c.Handle(context.Background(), offboardedEnvelope(eventID, tenantID))
	require.NoError(t, err)
	assert.Equal(t, []string{"success"}, metrics.results)
}

// ── EC-OFF-DB-DOWN-01 / EC-OFF-DLQ-01 ────────────────────────────────────────

// TestOffboardingConsumer_DBDown_Requeues verifies EC-OFF-DB-DOWN-01: when the
// DB is unavailable the cascade fails, Handle returns an error (SQS does NOT
// delete the message → redelivery). After MaxReceiveCount(5) failures SQS
// routes to the DLQ (EC-OFF-DLQ-01 — infrastructure behavior, verified
// indirectly here by the error-return contract).
func TestOffboardingConsumer_DBDown_Requeues(t *testing.T) {
	eventID, tenantID := uuid.New(), uuid.New()
	repo := &fakeCascadeDeleter{fn: func(context.Context, uuid.UUID) (int64, error) {
		return 0, errors.New("db unavailable")
	}}
	idem := &fakeIdempotencyStore{
		isProcessedFn: func(context.Context, uuid.UUID) (bool, error) { return false, nil },
		markProcessedFn: func(context.Context, uuid.UUID) error {
			t.Fatal("MarkProcessed must not be called when the cascade fails")
			return nil
		},
	}
	metrics := &fakeCascadeMetrics{}
	c := newTestConsumer(repo, idem, metrics)

	err := c.Handle(context.Background(), offboardedEnvelope(eventID, tenantID))
	require.Error(t, err, "DB down → Handle must return error so SQS requeues → eventually DLQ")
	assert.Equal(t, []string{"error"}, metrics.results)
}

// ── CONC-SQS-MULTI-01: two replicas same message → idempotency ───────────────

// TestOffboarding_ConcurrentReplicas_Idempotent covers CONC-SQS-MULTI-01:
// two consumer replicas receive the same SQS message simultaneously and both
// call IsProcessed before either transaction commits — both see false. The
// cascade is idempotent (DELETE is a no-op on already-deleted rows). Both
// MarkProcessed calls succeed (upsert-style behavior). The test verifies
// correctness: both handle calls complete without error and the cascade runs
// for the correct tenant on each replica.
func TestOffboarding_ConcurrentReplicas_Idempotent(t *testing.T) {
	eventID, tenantID := uuid.New(), uuid.New()

	var cascadeCalls atomic.Int32
	repo := &fakeCascadeDeleter{fn: func(_ context.Context, tid uuid.UUID) (int64, error) {
		cascadeCalls.Add(1)
		assert.Equal(t, tenantID, tid)
		return 3, nil
	}}
	// Both replicas see IsProcessed=false (simulating the race before
	// either transaction commits). MarkProcessed uses upsert semantics —
	// both succeed.
	idem := &fakeIdempotencyStore{
		isProcessedFn:   func(context.Context, uuid.UUID) (bool, error) { return false, nil },
		markProcessedFn: func(context.Context, uuid.UUID) error { return nil },
	}
	metrics := &fakeCascadeMetrics{}

	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			c := newTestConsumer(repo, idem, metrics)
			errs[idx] = c.Handle(context.Background(), offboardedEnvelope(eventID, tenantID))
		}(i)
	}
	wg.Wait()

	assert.NoError(t, errs[0])
	assert.NoError(t, errs[1])
	assert.Equal(t, int32(2), cascadeCalls.Load(), "both replicas cascade (idempotent DELETE)")
}
