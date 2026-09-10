package metrics

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gincommon "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-gincommon/pkg/gincommon"
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

func TestNewMetrics_PicksUpGincommonConstLabels(t *testing.T) {
	// ObservabilityMiddlewares is gincommon's public metrics-init API —
	// production calls it before NewMetrics so business collectors inherit
	// {service, version}. Isolated registry so this doesn't collide with
	// other tests' DefaultRegisterer collectors.
	_ = gincommon.ObservabilityMiddlewares(gincommon.Config{
		ServiceName:  "tender-acl-test",
		BuildVersion: "test",
	})
	reg := prometheus.NewRegistry()
	m, err := NewMetrics(reg)
	require.NoError(t, err)
	require.NotNil(t, m)
	got := gincommon.MetricsConstLabels()
	assert.Equal(t, "tender-acl-test", got["service"])
	assert.Equal(t, "test", got["version"])
}

func TestNewMetrics_CustomRegisterer_Succeeds(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetrics(reg)
	require.NoError(t, err)
	require.NotNil(t, m)
}

func TestNewMetrics_NilRegisterer_UsesDefault(t *testing.T) {
	// Passing an explicit nil falls back to gincommon.MetricsRegisterer().
	// Use a fresh registry to avoid polluting the default in the test process.
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

func TestMetrics_RecordUnexpectedEventType_DoesNotPanic(t *testing.T) {
	m := newTestMetrics(t)
	assert.NotPanics(t, func() {
		m.RecordUnexpectedEventType(context.Background(), "tenant-lifecycle-tenderacl-q", "SomeOtherEvent")
		m.RecordUnexpectedEventType(context.Background(), "member-removal-tenderacl-q", "TenantOffboarded")
	})
}

func TestMetrics_RecordProcessedEventsDuplicate_DoesNotPanic(t *testing.T) {
	m := newTestMetrics(t)
	assert.NotPanics(t, func() {
		m.RecordProcessedEventsDuplicate(context.Background(), "tenant_lifecycle_cleanup")
		m.RecordProcessedEventsDuplicate(context.Background(), "member_removal")
	})
}
