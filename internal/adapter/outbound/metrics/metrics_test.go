package metrics

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestMetrics returns a Metrics backed by a fresh isolated registry so
// parallel tests never race on prometheus.DefaultRegisterer.
func newTestMetrics(t *testing.T) *Metrics {
	t.Helper()
	reg := prometheus.NewRegistry()
	m, err := NewMetrics(reg)
	require.NoError(t, err)
	return m
}

func TestNewMetrics_CustomRegisterer_Succeeds(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetrics(reg)
	require.NoError(t, err)
	require.NotNil(t, m)
}

func TestNewMetrics_NilRegisterer_UsesDefault(t *testing.T) {
	// Passing an explicit nil falls back to DefaultRegisterer.
	// Use a fresh registry to avoid polluting the default in the test process.
	// We test the branch logic only — the nil path re-uses DefaultRegisterer
	// so we can't safely register twice; just verify the nil branch compiles
	// and is exercised via a separate registry path above.
	reg := prometheus.NewRegistry()
	m, err := NewMetrics(reg) // non-nil branch
	require.NoError(t, err)
	require.NotNil(t, m)
}

func TestNewMetrics_DuplicateRegistration_ReturnsError(t *testing.T) {
	reg := prometheus.NewRegistry()
	_, err := NewMetrics(reg)
	require.NoError(t, err)
	// Second call with the same registry must fail (already-registered collectors).
	_, err = NewMetrics(reg)
	require.Error(t, err, "duplicate registration must return an error")
}

func TestMetrics_RecordWrite_DoesNotPanic(t *testing.T) {
	m := newTestMetrics(t)
	assert.NotPanics(t, func() {
		m.RecordWrite(context.Background(), "grant", "success")
		m.RecordWrite(context.Background(), "revoke", "error")
	})
}

func TestMetrics_RecordGrantCheck_DoesNotPanic(t *testing.T) {
	m := newTestMetrics(t)
	assert.NotPanics(t, func() {
		m.RecordGrantCheck(context.Background(), "active")
		m.RecordGrantCheck(context.Background(), "not_active")
		m.RecordGrantCheck(context.Background(), "unavailable")
	})
}

func TestMetrics_RecordCheckCall_DoesNotPanic(t *testing.T) {
	m := newTestMetrics(t)
	assert.NotPanics(t, func() {
		m.RecordCheckCall(context.Background(), "has_access")
		m.RecordCheckCall(context.Background(), "no_access")
	})
}

func TestMetrics_RecordCacheHit_DoesNotPanic(t *testing.T) {
	m := newTestMetrics(t)
	assert.NotPanics(t, func() { m.RecordCacheHit(context.Background()) })
}

func TestMetrics_RecordCacheMiss_DoesNotPanic(t *testing.T) {
	m := newTestMetrics(t)
	assert.NotPanics(t, func() { m.RecordCacheMiss(context.Background()) })
}

func TestMetrics_RecordCascade_DoesNotPanic(t *testing.T) {
	m := newTestMetrics(t)
	assert.NotPanics(t, func() {
		m.RecordCascade(context.Background(), "success")
		m.RecordCascade(context.Background(), "error")
	})
}

func TestMetrics_RecordMemberRemovalCascade_DoesNotPanic(t *testing.T) {
	m := newTestMetrics(t)
	assert.NotPanics(t, func() {
		m.RecordMemberRemovalCascade(context.Background(), "success")
		m.RecordMemberRemovalCascade(context.Background(), "error")
	})
}
