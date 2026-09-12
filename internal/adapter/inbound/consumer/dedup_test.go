package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/BCBP-SOLUTIONS-FZC-LLC/iam-tender-acl/internal/core/port"
	events "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-events/pkg/events"
)

func TestSkipDuplicate_IncrementsMetricOnHit(t *testing.T) {
	eventID := uuid.New()
	idem := &fakeIdempotencyStore{
		isProcessedFn: func(context.Context, uuid.UUID) (bool, error) { return true, nil },
	}
	metrics := &fakeCascadeMetrics{}

	hit, err := skipDuplicate(context.Background(), idem, metrics, offboardingConsumerName, eventID)
	require.NoError(t, err)
	assert.True(t, hit)
	assert.Equal(t, []string{"duplicate:tenant_lifecycle_cleanup"}, metrics.results)

	idem.isProcessedFn = func(context.Context, uuid.UUID) (bool, error) { return false, nil }
	metrics.results = nil
	hit, err = skipDuplicate(context.Background(), idem, metrics, offboardingConsumerName, eventID)
	require.NoError(t, err)
	assert.False(t, hit)
	assert.Empty(t, metrics.results)
}

func TestSkipDuplicate_IsProcessedErrorPropagates(t *testing.T) {
	wantErr := errors.New("dedup store unavailable")
	idem := &fakeIdempotencyStore{
		isProcessedFn: func(context.Context, uuid.UUID) (bool, error) { return false, wantErr },
	}

	hit, err := skipDuplicate(context.Background(), idem, &fakeCascadeMetrics{}, offboardingConsumerName, uuid.New())
	require.ErrorIs(t, err, wantErr)
	assert.False(t, hit)
}

func TestAckUnknown_MarksProcessed(t *testing.T) {
	eventID := uuid.New()
	var marked uuid.UUID
	idem := &fakeIdempotencyStore{
		markProcessedFn: func(_ context.Context, id uuid.UUID) error {
			marked = id
			return nil
		},
	}
	metrics := &fakeCascadeMetrics{}
	env := events.Envelope[json.RawMessage]{ID: eventID.String(), Type: "NeverHeardOfThis"}

	err := ackUnknown(context.Background(), fakeTxRunner{}, idem, metrics, port.SlogStyleLogger{}, tenantLifecycleQueueName, env)
	require.NoError(t, err)
	assert.Equal(t, eventID, marked)
	assert.Equal(t, []string{"tenant-lifecycle-tenderacl-q:NeverHeardOfThis"}, metrics.results)
}

func TestAckUnknown_InvalidID_AcksWithoutMark(t *testing.T) {
	idem := &fakeIdempotencyStore{
		markProcessedFn: func(context.Context, uuid.UUID) error {
			t.Fatal("MarkProcessed must not run for a non-UUID envelope id")
			return nil
		},
	}
	env := events.Envelope[json.RawMessage]{ID: "not-a-uuid", Type: "NeverHeardOfThis"}

	err := ackUnknown(context.Background(), fakeTxRunner{}, idem, &fakeCascadeMetrics{}, port.SlogStyleLogger{}, tenantLifecycleQueueName, env)
	require.NoError(t, err)
}

func TestMarkProcessedInTx_WrapsMarkProcessedError(t *testing.T) {
	wantErr := errors.New("insert conflict")
	idem := &fakeIdempotencyStore{
		markProcessedFn: func(context.Context, uuid.UUID) error { return wantErr },
	}

	err := markProcessedInTx(context.Background(), fakeTxRunner{}, idem, uuid.New())
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "mark processed")
}
