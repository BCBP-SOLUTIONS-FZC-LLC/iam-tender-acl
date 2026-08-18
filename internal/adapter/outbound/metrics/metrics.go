// Package metrics registers every Prometheus instrument this service emits,
// named per tender-acl-service-lld.md §14.2. Uses prometheus/client_golang
// directly, registered onto gincommon.MetricsRegisterer() (see NewMetrics) —
// the same registry platform-gincommon's own HTTP metrics and this
// process's /metrics endpoint (promhttp.Handler(), cmd/tender-acl/main.go)
// already share — rather than a parallel OpenTelemetry metrics pipeline,
// matching every sibling IAM service's convention (iam-org-membership,
// iam-catalog-admin, iam-user-profile).
package metrics

import (
	"context"
	"strconv"

	"github.com/prometheus/client_golang/prometheus"
)

// requestDurationBuckets must include 0.020 exactly — deploy/monitoring's
// SLO-1 recording rule (tender_acl:tac4_latency_sli:ratio_rate_5m) queries
// tender_acl_request_duration_seconds_bucket{le="0.020"} directly; any
// bucket set missing that exact boundary silently breaks the recording
// rule (no data, not an error) rather than the underlying SLO burn-rate
// alerts firing incorrectly.
var requestDurationBuckets = []float64{0.001, 0.002, 0.005, 0.01, 0.02, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// cacheKeyLabel is the fixed value of the LLD §14.2 cache metrics'
// {key} label — there is exactly one cache region (tac:acl, LLD §9), so
// the label carries a constant, not the per-tuple cache key string (which
// would be unbounded-cardinality); it exists so a second region added
// later has somewhere to go without a metric rename.
const cacheKeyLabel = "tac:acl"

// Metrics holds every Prometheus instrument this service emits.
type Metrics struct {
	requestsTotal             *prometheus.CounterVec
	requestDuration           *prometheus.HistogramVec
	writesTotal               *prometheus.CounterVec
	grantChecksTotal          *prometheus.CounterVec
	checkCallsTotal           *prometheus.CounterVec
	cacheHitsTotal            *prometheus.CounterVec
	cacheMissesTotal          *prometheus.CounterVec
	cascadeTotal              *prometheus.CounterVec
	memberRemovalCascadeTotal *prometheus.CounterVec
}

// NewMetrics builds and registers every tender_acl_* instrument. Call once
// at startup, before /metrics is served.
//
// reg is optional (variadic so every existing zero-arg call site keeps
// compiling unchanged) and defaults to prometheus.DefaultRegisterer — the
// registry these collectors were always implicitly registered against.
// cmd/tender-acl/main.go passes gincommon.MetricsRegisterer() so these
// collectors are explicitly confirmed to land in the same registry as
// platform-gincommon's own HTTP metrics, rather than assuming
// prometheus.DefaultRegisterer and hoping gincommon agrees (gincommon
// v1.3.0 added MetricsRegisterer specifically so callers don't have to
// guess — see iam-user-profile's identical internal/adapter/outbound/metrics
// business.go.Register for precedent). Test call sites (no gin engine, so
// nothing ever calls ObservabilityMiddlewares) pass nothing and get today's
// behavior unchanged.
func NewMetrics(reg ...prometheus.Registerer) (*Metrics, error) {
	registerer := prometheus.DefaultRegisterer
	if len(reg) > 0 && reg[0] != nil {
		registerer = reg[0]
	}
	m := &Metrics{
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "tender_acl_requests_total",
				Help: "Total HTTP requests handled, by method/path/status",
			},
			[]string{"method", "path", "status"},
		),
		requestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "tender_acl_request_duration_seconds",
				Help:    "HTTP request duration in seconds, by method/path",
				Buckets: requestDurationBuckets,
			},
			[]string{"method", "path"},
		),
		writesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "tender_acl_writes_total",
				Help: "Total grant/revoke writes, by op and result",
			},
			[]string{"op", "result"},
		),
		grantChecksTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "tender_acl_grant_checks_total",
				Help: "Total TAC-2 grant-time membership checks, by status (active/not_active/unavailable)",
			},
			[]string{"status"},
		),
		checkCallsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "tender_acl_check_calls_total",
				Help: "Total TAC-4 access checks, by status (has_access/no_access)",
			},
			[]string{"status"},
		),
		// LLD §14.2's tender_acl_cache_hit_ratio{key} is derived from these
		// two raw counters via PromQL
		// (rate(tender_acl_cache_hits_total)/(rate(hits)+rate(misses))) —
		// a raw ratio gauge would be redundant with them and isn't itself
		// instrumented, matching the identical pattern already used for
		// iam-catalog-admin's cat:departments/cat:plans cache. "Miss"
		// means TAC-4's CheckAccess got a clean cache miss (key absent);
		// a Valkey read error (cache unavailable, LLD §9.4) is logged
		// separately and counted in neither series, since it's a
		// different failure mode from an ordinary miss.
		cacheHitsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "tender_acl_cache_hits_total",
				Help: "Cache hits against this service's own tac:acl cache, labeled by key.",
			},
			[]string{"key"},
		),
		cacheMissesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "tender_acl_cache_misses_total",
				Help: "Cache misses against this service's own tac:acl cache, labeled by key.",
			},
			[]string{"key"},
		),
		cascadeTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "tender_acl_tenant_offboarding_cascade_total",
				Help: "Total tenant offboarding cascade deletions, by result",
			},
			[]string{"result"},
		),
		// ADR-0007 Wave 3 Phase 3 (O_AND_M_DELTA.md §5 Option B) — a separate
		// counter from cascadeTotal above, not a shared metric with an extra
		// label: the tenant-offboarding and per-user-removal cascades are
		// different failure domains (different queues, different DLQs,
		// different alert runbooks) and conflating them into one series would
		// make each individually harder to reason about during an incident.
		memberRemovalCascadeTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "tender_acl_member_removal_cascade_total",
				Help: "Total per-user-removal ACL cascade soft-deletes, by result",
			},
			[]string{"result"},
		),
	}

	for _, c := range []prometheus.Collector{
		m.requestsTotal, m.requestDuration, m.writesTotal,
		m.grantChecksTotal, m.checkCallsTotal, m.cacheHitsTotal, m.cacheMissesTotal,
		m.cascadeTotal, m.memberRemovalCascadeTotal,
	} {
		if err := registerer.Register(c); err != nil {
			return nil, err
		}
	}

	// Pre-initialize the one known cache-key label so dashboards show 0
	// rather than "no data" before the first request.
	m.cacheHitsTotal.WithLabelValues(cacheKeyLabel)
	m.cacheMissesTotal.WithLabelValues(cacheKeyLabel)

	return m, nil
}

// RecordRequest increments tender_acl_requests_total, tagged by method/route/status.
func (m *Metrics) RecordRequest(_ context.Context, method, route string, status int) {
	m.requestsTotal.WithLabelValues(method, route, strconv.Itoa(status)).Inc()
}

// RecordRequestDuration records tender_acl_request_duration_seconds, tagged by method/route.
func (m *Metrics) RecordRequestDuration(_ context.Context, method, route string, seconds float64) {
	m.requestDuration.WithLabelValues(method, route).Observe(seconds)
}

// RecordWrite increments tender_acl_writes_total for a grant/revoke op, tagged by result.
func (m *Metrics) RecordWrite(_ context.Context, op, result string) {
	m.writesTotal.WithLabelValues(op, result).Inc()
}

// RecordGrantCheck increments tender_acl_grant_checks_total, tagged by status
// (active/not_active/unavailable).
func (m *Metrics) RecordGrantCheck(_ context.Context, status string) {
	m.grantChecksTotal.WithLabelValues(status).Inc()
}

// RecordCheckCall increments tender_acl_check_calls_total, tagged by status
// (has_access/no_access).
func (m *Metrics) RecordCheckCall(_ context.Context, status string) {
	m.checkCallsTotal.WithLabelValues(status).Inc()
}

// RecordCacheHit increments tender_acl_cache_hits_total for TAC-4's tac:acl
// cache (LLD §9/§14.2).
func (m *Metrics) RecordCacheHit(_ context.Context) {
	m.cacheHitsTotal.WithLabelValues(cacheKeyLabel).Inc()
}

// RecordCacheMiss increments tender_acl_cache_misses_total for TAC-4's
// tac:acl cache (LLD §9/§14.2). Not called on a Valkey read error — see
// NewMetrics' doc comment on cacheMissesTotal.
func (m *Metrics) RecordCacheMiss(_ context.Context) {
	m.cacheMissesTotal.WithLabelValues(cacheKeyLabel).Inc()
}

// RecordCascade implements consumer.CascadeMetrics, so main.go can pass a
// *Metrics directly to the offboarding consumer.
func (m *Metrics) RecordCascade(_ context.Context, result string) {
	m.cascadeTotal.WithLabelValues(result).Inc()
}

// RecordMemberRemovalCascade implements consumer.MemberRemovalMetrics, so
// main.go can pass a *Metrics directly to the member removal consumer.
func (m *Metrics) RecordMemberRemovalCascade(_ context.Context, result string) {
	m.memberRemovalCascadeTotal.WithLabelValues(result).Inc()
}
