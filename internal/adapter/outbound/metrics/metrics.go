// Package metrics registers every tender-acl-specific Prometheus instrument
// this service emits, named per docs/lld/iam-lld-tender-acl-service.md §14.2. Uses
// prometheus/client_golang directly, registered onto
// gincommon.MetricsRegisterer() after ObservabilityMiddlewares has primed
// gincommon's metrics-init API (see NewMetrics) — the same registry and
// {service, version} const labels platform-gincommon's own HTTP metrics
// and this process's /metrics endpoint (promhttp.Handler(), cmd/tender-acl/
// main.go) already share — rather than a parallel OpenTelemetry metrics
// pipeline, matching iam-org-membership, iam-realm-provisioner, and
// iam-catalog-admin.
//
// Generic per-request HTTP metrics (count/duration/status, by
// method+route) are deliberately NOT reimplemented here: gincommon's own
// ObservabilityMiddlewares already records those as http_requests_total/
// http_request_duration_seconds for every route this service serves, so
// this package fully passes that through rather than duplicating it under
// a tender_acl_* name. This service is not yet deployed, so making that
// switch carried no live-dashboard cost; deploy/monitoring's SLO/alert/HPA
// rules and the release canary check were updated to gincommon's metric
// names in the same change (see CHANGELOG.md). Everything below is a
// metric gincommon has no equivalent for: business-level write/grant-check/
// cache/cascade outcomes.
package metrics

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	gincommon "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-gincommon/pkg/gincommon"
)

// cacheKeyLabel is the fixed value of the LLD §14.2 cache metrics'
// {key} label — there is exactly one cache region (tac:acl, LLD §9), so
// the label carries a constant, not the per-tuple cache key string (which
// would be unbounded-cardinality); it exists so a second region added
// later has somewhere to go without a metric rename.
const cacheKeyLabel = "tac:acl"

// Metrics holds every tender-acl-specific Prometheus instrument this
// service emits. Generic per-request HTTP metrics are not among them — see
// the package doc comment.
type Metrics struct {
	writesTotal                    *prometheus.CounterVec
	grantChecksTotal               *prometheus.CounterVec
	checkCallsTotal                *prometheus.CounterVec
	cacheHitsTotal                 *prometheus.CounterVec
	cacheMissesTotal               *prometheus.CounterVec
	cascadeTotal                   *prometheus.CounterVec
	memberRemovalCascadeTotal      *prometheus.CounterVec
	unexpectedEventTypeTotal       *prometheus.CounterVec
	processedEventsDuplicatesTotal *prometheus.CounterVec
}

// gincommonLabels returns a copy of gincommon's {service, version} const
// labels so business collectors scrape on the same registry and labels as
// HTTP metrics. Returns nil when ObservabilityMiddlewares has not run yet,
// matching prometheus's "no const labels" zero value so unit tests that
// never bootstrap gincommon still register cleanly. Same helper
// iam-org-membership's metrics.Register uses.
func gincommonLabels() prometheus.Labels {
	labels := gincommon.MetricsConstLabels()
	if len(labels) == 0 {
		return nil
	}
	return labels
}

// NewMetrics builds and registers every tender_acl_* instrument. Call once
// at startup AFTER gincommon.ObservabilityMiddlewares has run (so
// MetricsRegisterer / MetricsConstLabels are the real gincommon registry
// and {service, version} labels, not the DefaultRegisterer fallback) and
// before /metrics is served — matching iam-org-membership and
// iam-realm-provisioner.
//
// reg is optional (variadic so every existing zero-arg call site keeps
// compiling unchanged) and defaults to gincommon.MetricsRegisterer().
// Test call sites (no gin engine, so nothing ever calls
// ObservabilityMiddlewares) pass an isolated registry.
func NewMetrics(reg ...prometheus.Registerer) (*Metrics, error) {
	registerer := gincommon.MetricsRegisterer()
	if len(reg) > 0 && reg[0] != nil {
		registerer = reg[0]
	}
	labels := gincommonLabels()
	m := &Metrics{
		writesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:        "tender_acl_writes_total",
				Help:        "Total grant/revoke writes, by op and result",
				ConstLabels: labels,
			},
			[]string{"op", "result"},
		),
		grantChecksTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:        "tender_acl_grant_checks_total",
				Help:        "Total TAC-2 grant-time membership checks, by status (active/not_active/unavailable)",
				ConstLabels: labels,
			},
			[]string{"status"},
		),
		checkCallsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:        "tender_acl_check_calls_total",
				Help:        "Total TAC-4 access checks, by status (has_access/no_access)",
				ConstLabels: labels,
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
				Name:        "tender_acl_cache_hits_total",
				Help:        "Cache hits against this service's own tac:acl cache, labeled by key.",
				ConstLabels: labels,
			},
			[]string{"key"},
		),
		cacheMissesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:        "tender_acl_cache_misses_total",
				Help:        "Cache misses against this service's own tac:acl cache, labeled by key.",
				ConstLabels: labels,
			},
			[]string{"key"},
		),
		cascadeTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:        "tender_acl_tenant_offboarding_cascade_total",
				Help:        "Total tenant offboarding cascade deletions, by result",
				ConstLabels: labels,
			},
			[]string{"result"},
		),
		// ADR-0007 Wave 3 Phase 3 — a separate
		// counter from cascadeTotal above, not a shared metric with an extra
		// label: the tenant-offboarding and per-user-removal cascades are
		// different failure domains (different queues, different DLQs,
		// different alert runbooks) and conflating them into one series would
		// make each individually harder to reason about during an incident.
		memberRemovalCascadeTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:        "tender_acl_member_removal_cascade_total",
				Help:        "Total per-user-removal ACL cascade soft-deletes, by result",
				ConstLabels: labels,
			},
			[]string{"result"},
		),
		// Mirrors iam-org-membership's iam_unknown_event_acknowledged_total —
		// both consumers ack-and-drop (never error/retry) any event_type they
		// don't recognize, per their own doc comments; this is what makes
		// that silent skip observable instead of invisible. A sustained
		// nonzero rate means either a producer rename this service's own
		// tenantMembershipsPurgedEventType/membershipRevokedEventType
		// constants haven't caught up to (exactly what happened before those
		// constants existed — see CHANGELOG.md) or genuine queue
		// misconfiguration (wrong event routed to this queue).
		unexpectedEventTypeTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:        "tender_acl_unexpected_event_type_total",
				Help:        "Events acknowledged and dropped because event_type didn't match what this queue's consumer expects, by queue and event_type.",
				ConstLabels: labels,
			},
			[]string{"queue", "event_type"},
		),
		// Mirrors iam-org-membership's iam_processed_events_duplicates_total
		// (IDEMP-4) — skipDuplicate increments this when Envelope.ID is
		// already in processed_events, so at-least-once SQS redelivery is
		// observable rather than only a log line.
		processedEventsDuplicatesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:        "tender_acl_processed_events_duplicates_total",
				Help:        "SQS deliveries skipped because processed_events already recorded the envelope id, by consumer.",
				ConstLabels: labels,
			},
			[]string{"consumer"},
		),
	}

	for _, c := range []prometheus.Collector{
		m.writesTotal,
		m.grantChecksTotal, m.checkCallsTotal, m.cacheHitsTotal, m.cacheMissesTotal,
		m.cascadeTotal, m.memberRemovalCascadeTotal, m.unexpectedEventTypeTotal,
		m.processedEventsDuplicatesTotal,
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

// RecordUnexpectedEventType implements both consumer.CascadeMetrics and
// consumer.MemberRemovalMetrics' identical method of this name, so main.go
// can pass the same *Metrics to both consumers. queue is the SQS queue name
// (tenant-lifecycle-tenderacl-q / member-removal-tenderacl-q); eventType is
// the envelope's raw, unrecognized Type value.
func (m *Metrics) RecordUnexpectedEventType(_ context.Context, queue, eventType string) {
	m.unexpectedEventTypeTotal.WithLabelValues(queue, eventType).Inc()
}

// RecordProcessedEventsDuplicate implements both consumer.CascadeMetrics and
// consumer.MemberRemovalMetrics' identical method of this name (IDEMP-4).
func (m *Metrics) RecordProcessedEventsDuplicate(_ context.Context, consumer string) {
	m.processedEventsDuplicatesTotal.WithLabelValues(consumer).Inc()
}
