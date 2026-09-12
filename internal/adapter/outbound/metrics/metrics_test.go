package metrics

import (
	"context"
	"strings"
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
	m, err := NewMetrics("test", reg)
	require.NoError(t, err)
	return m
}

func TestNewMetrics_PicksUpGincommonConstLabels(t *testing.T) {
	// ObservabilityMiddlewares is gincommon's public metrics-init API —
	// production calls it before NewMetrics so Tier 3 collectors inherit
	// {service, version}. Isolated registry so this doesn't collide with
	// other tests' DefaultRegisterer collectors.
	_ = gincommon.ObservabilityMiddlewares(gincommon.Config{
		ServiceName:  "tender-acl-test",
		BuildVersion: "test",
	})
	reg := prometheus.NewRegistry()
	m, err := NewMetrics("test", reg)
	require.NoError(t, err)
	require.NotNil(t, m)
	got := gincommon.MetricsConstLabels()
	assert.Equal(t, "tender-acl-test", got["service"])
	assert.Equal(t, "test", got["version"])
}

func TestNewMetrics_CustomRegisterer_Succeeds(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetrics("test", reg)
	require.NoError(t, err)
	require.NotNil(t, m)
}

func TestNewMetrics_NilRegisterer_UsesDefault(t *testing.T) {
	// Passing an explicit nil falls back to gincommon.MetricsRegisterer().
	// Use a fresh registry to avoid polluting the default in the test process.
	reg := prometheus.NewRegistry()
	m, err := NewMetrics("test", reg) // non-nil branch
	require.NoError(t, err)
	require.NotNil(t, m)
}

func TestNewMetrics_DuplicateRegistration_ReturnsError(t *testing.T) {
	reg := prometheus.NewRegistry()
	_, err := NewMetrics("test", reg)
	require.NoError(t, err)
	// Second call with the same registry must fail (already-registered collectors).
	_, err = NewMetrics("test", reg)
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

// TestMetrics_CascadeBothEventTypes_ShareOneSeries validates that RecordCascade
// and RecordMemberRemovalCascade write to the same iam_cascade_operations_total
// series with distinct event_type label values.
func TestMetrics_CascadeBothEventTypes_ShareOneSeries(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetrics("test", reg)
	require.NoError(t, err)

	ctx := context.Background()
	m.RecordCascade(ctx, "success")
	m.RecordMemberRemovalCascade(ctx, "error")

	mfs, err := reg.Gather()
	require.NoError(t, err)

	for _, mf := range mfs {
		if mf.GetName() != "iam_cascade_operations_total" {
			continue
		}
		// Two distinct event_type values must be present.
		assert.Len(t, mf.GetMetric(), 2, "both event_type values must be present")
		eventTypes := make(map[string]bool)
		for _, metric := range mf.GetMetric() {
			for _, lp := range metric.GetLabel() {
				if lp.GetName() == "event_type" {
					eventTypes[lp.GetValue()] = true
				}
			}
		}
		assert.True(t, eventTypes[cascadeEventTypeOffboarding],
			"TenantMembershipsPurged event_type must be present")
		assert.True(t, eventTypes[cascadeEventTypeMemberRemoval],
			"MembershipRevoked event_type must be present")
		return
	}
	t.Fatal("iam_cascade_operations_total not found in gathered metrics")
}

// TestMetrics_NamingConventions is a CI gate that validates every metric this
// package registers against the Enterprise Platform Observability Standard:
//
//   - Namespace: platform_* (Tier 1), iam_* (Tier 2), iam_tender_acl_* (Tier 3)
//   - Counter suffix: _total
//   - Histogram suffix: _seconds
//   - No high-cardinality labels (user_id, email, tenant_id, request_id, etc.)
//   - Tier 2 required const labels: service, environment
func TestMetrics_NamingConventions(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetrics("test", reg)
	require.NoError(t, err)

	// Touch every metric so all series are present in Gather() output.
	ctx := context.Background()
	m.RecordWrite(ctx, "grant", "success")
	m.RecordGrantCheck(ctx, "active")
	m.RecordCheckCall(ctx, "has_access")
	m.RecordCacheHit(ctx)
	m.RecordCacheMiss(ctx)
	m.RecordCascade(ctx, "success")
	m.RecordMemberRemovalCascade(ctx, "success")
	m.RecordUnexpectedEventType(ctx, "q", "T")
	m.RecordProcessedEventsDuplicate(ctx, "c")

	mfs, err := reg.Gather()
	require.NoError(t, err)
	require.NotEmpty(t, mfs, "no metrics gathered — registration must have succeeded")

	bannedLabels := map[string]bool{
		"user_id": true, "email": true, "tenant_id": true,
		"request_id": true, "event_id": true, "session_id": true,
	}

	// Tier 2 metrics must carry service and environment const labels.
	tier2RequiredLabels := map[string]bool{"service": true, "environment": true}

	for _, mf := range mfs {
		name := mf.GetName()
		isTier1 := strings.HasPrefix(name, "platform_")
		isTier3 := strings.HasPrefix(name, "iam_tender_acl_")
		isTier2 := strings.HasPrefix(name, "iam_") && !isTier3

		// Rule: namespace classification
		assert.True(t, isTier1 || isTier2 || isTier3,
			"metric %q must use platform_, iam_, or iam_tender_acl_ namespace", name)

		// Rule: counter suffix
		if mf.GetType().String() == "COUNTER" {
			assert.True(t, strings.HasSuffix(name, "_total"),
				"counter %q must end with _total", name)
		}

		// Rule: histogram suffix
		if mf.GetType().String() == "HISTOGRAM" {
			assert.True(t, strings.HasSuffix(name, "_seconds") || strings.HasSuffix(name, "_bytes"),
				"histogram %q must end with _seconds or _bytes", name)
		}

		for _, metric := range mf.GetMetric() {
			labelNames := make(map[string]bool)
			for _, lp := range metric.GetLabel() {
				ln := lp.GetName()
				// Rule: no high-cardinality labels
				assert.False(t, bannedLabels[ln],
					"metric %q has prohibited high-cardinality label %q", name, ln)
				labelNames[ln] = true
			}

			// Rule: Tier 2 required const labels
			if isTier2 {
				for required := range tier2RequiredLabels {
					assert.True(t, labelNames[required],
						"Tier 2 metric %q missing required label %q", name, required)
				}
			}
		}
	}
}
