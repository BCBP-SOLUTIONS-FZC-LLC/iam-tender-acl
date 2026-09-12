// Package metrics registers every tender-acl Prometheus instrument this
// service emits, classified by the Enterprise Platform Observability Standard
// three-tier taxonomy.
//
// Tier 2 — IAM domain metrics (iam_*): semantics shared across multiple IAM
// services; required const labels are service and environment. Instruments:
// iam_cascade_operations_total, iam_unknown_event_acknowledged_total.
//
// Tier 3 — service-specific metrics (iam_tender_acl_*): concepts unique to
// this service; const labels are gincommon's {service, version}. Instruments:
// iam_tender_acl_writes_total, iam_tender_acl_grant_checks_total,
// iam_tender_acl_check_calls_total, iam_tender_acl_cache_hits_total,
// iam_tender_acl_cache_misses_total, iam_tender_acl_processed_events_duplicates_total.
//
// Note: platform_duplicate_messages_total has been proposed (registry-proposed
// per the standard) to replace iam_tender_acl_processed_events_duplicates_total.
// Adoption is blocked on Platform Observability Registry ratification.
//
// Generic HTTP metrics (http_requests_total / http_request_duration_seconds)
// and SQS consumer metrics (events_consumed_total etc.) are emitted by
// platform-gincommon and platform-events respectively — not duplicated here.
package metrics

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"

	gincommon "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-gincommon/pkg/gincommon"
)

// cacheKeyLabel is the fixed value of the cache metrics' {key} label.
// There is exactly one cache region (tac:acl, LLD §9); the label exists so
// a second region added later has somewhere to go without a metric rename.
const cacheKeyLabel = "tac:acl"

// Cascade event_type label values: these differentiate the two cascade
// consumers inside the shared iam_cascade_operations_total series, preserving
// independent observability without requiring separate metric names.
const (
	cascadeEventTypeOffboarding   = "TenantMembershipsPurged"
	cascadeEventTypeMemberRemoval = "MembershipRevoked"
)

// Metrics holds every tender-acl Prometheus instrument. Generic HTTP and
// SQS consumer metrics are not among them — see the package doc comment.
type Metrics struct {
	// Tier 2: IAM domain metrics (iam_*)
	cascadeOperationsTotal        *prometheus.CounterVec
	unknownEventAcknowledgedTotal *prometheus.CounterVec

	// Tier 3: service-specific metrics (iam_tender_acl_*)
	writesTotal                    *prometheus.CounterVec
	grantChecksTotal               *prometheus.CounterVec
	checkCallsTotal                *prometheus.CounterVec
	cacheHitsTotal                 *prometheus.CounterVec
	cacheMissesTotal               *prometheus.CounterVec
	processedEventsDuplicatesTotal *prometheus.CounterVec
}

// gincommonLabels returns a copy of gincommon's {service, version} const
// labels so Tier 3 collectors share the same label set as HTTP metrics.
// Returns nil when ObservabilityMiddlewares has not run yet; prometheus treats
// nil as "no const labels" so unit tests that never bootstrap gincommon still
// register cleanly. Same helper iam-org-membership's metrics.Register uses.
func gincommonLabels() prometheus.Labels {
	labels := gincommon.MetricsConstLabels()
	if len(labels) == 0 {
		return nil
	}
	return labels
}

// NewMetrics builds and registers all instruments. environment is injected as
// a required const label on Tier 2 (IAM domain) metrics per the Enterprise
// Platform Observability Standard. Call once at startup AFTER
// gincommon.ObservabilityMiddlewares has run (so Tier 3 metrics inherit the
// real {service, version} const labels) and before /metrics is served —
// matching iam-org-membership and iam-realm-provisioner.
//
// reg is optional (variadic so every existing zero-arg call site keeps
// compiling unchanged) and defaults to gincommon.MetricsRegisterer(). Test
// call sites pass an isolated registry.
func NewMetrics(environment string, reg ...prometheus.Registerer) (*Metrics, error) {
	registerer := gincommon.MetricsRegisterer()
	if len(reg) > 0 && reg[0] != nil {
		registerer = reg[0]
	}

	// Tier 2 required const labels (service + environment per the standard).
	tier2 := prometheus.Labels{
		"service":     "tender-acl",
		"environment": environment,
	}

	// Tier 3 uses gincommon's {service, version} const labels to match
	// the label set that platform-gincommon's HTTP metrics carry.
	tier3 := gincommonLabels()

	m := &Metrics{
		// ── Tier 2: IAM domain ────────────────────────────────────────────

		// iam_cascade_operations_total unifies the two cascade consumers
		// (tenant offboarding + per-user membership removal) into one IAM
		// domain metric. The event_type label differentiates the two, so
		// each failure domain remains independently observable and alertable
		// without requiring separate metric names.
		cascadeOperationsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:        "iam_cascade_operations_total",
				Help:        "ACL cascade operations triggered by inbound IAM events, by event_type and result.",
				ConstLabels: tier2,
			},
			[]string{"event_type", "result"},
		),

		// iam_unknown_event_acknowledged_total mirrors the identical metric
		// in iam-org-membership: both consumers ack-and-drop any event_type
		// they don't recognize (never error/retry). A sustained nonzero rate
		// signals a producer rename or queue misconfiguration.
		unknownEventAcknowledgedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:        "iam_unknown_event_acknowledged_total",
				Help:        "Events acknowledged and dropped because event_type did not match what this queue's consumer expects.",
				ConstLabels: tier2,
			},
			[]string{"queue", "event_type"},
		),

		// ── Tier 3: service-specific ──────────────────────────────────────

		writesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:        "iam_tender_acl_writes_total",
				Help:        "Total grant/revoke writes, by op and result.",
				ConstLabels: tier3,
			},
			[]string{"op", "result"},
		),
		grantChecksTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:        "iam_tender_acl_grant_checks_total",
				Help:        "TAC-2 grant-time membership checks, by status (active/not_active/unavailable).",
				ConstLabels: tier3,
			},
			[]string{"status"},
		),
		checkCallsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:        "iam_tender_acl_check_calls_total",
				Help:        "TAC-4 access checks, by status (has_access/no_access).",
				ConstLabels: tier3,
			},
			[]string{"status"},
		),
		// LLD §14.2: iam_tender_acl_cache_hit_ratio{key} is derived from these
		// two counters via PromQL (rate(hits) / (rate(hits)+rate(misses))).
		// A Valkey read error is logged separately and counted in neither
		// series — it is a different failure mode from an ordinary miss.
		cacheHitsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:        "iam_tender_acl_cache_hits_total",
				Help:        "Cache hits against the tac:acl Valkey region, by key.",
				ConstLabels: tier3,
			},
			[]string{"key"},
		),
		cacheMissesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:        "iam_tender_acl_cache_misses_total",
				Help:        "Cache misses against the tac:acl Valkey region, by key.",
				ConstLabels: tier3,
			},
			[]string{"key"},
		),
		// Note: platform_duplicate_messages_total has been proposed as the
		// canonical platform-tier name for this concept but requires Platform
		// Observability Registry ratification before broad adoption. Using
		// iam_tender_acl_processed_events_duplicates_total until approved.
		processedEventsDuplicatesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name:        "iam_tender_acl_processed_events_duplicates_total",
				Help:        "SQS deliveries skipped because processed_events already recorded the envelope ID, by consumer.",
				ConstLabels: tier3,
			},
			[]string{"consumer"},
		),
	}

	for _, c := range []prometheus.Collector{
		m.cascadeOperationsTotal,
		m.unknownEventAcknowledgedTotal,
		m.writesTotal,
		m.grantChecksTotal,
		m.checkCallsTotal,
		m.cacheHitsTotal,
		m.cacheMissesTotal,
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

// RecordWrite increments iam_tender_acl_writes_total for a grant/revoke op.
func (m *Metrics) RecordWrite(_ context.Context, op, result string) {
	m.writesTotal.WithLabelValues(op, result).Inc()
}

// RecordGrantCheck increments iam_tender_acl_grant_checks_total by status
// (active/not_active/unavailable).
func (m *Metrics) RecordGrantCheck(_ context.Context, status string) {
	m.grantChecksTotal.WithLabelValues(status).Inc()
}

// RecordCheckCall increments iam_tender_acl_check_calls_total by status
// (has_access/no_access).
func (m *Metrics) RecordCheckCall(_ context.Context, status string) {
	m.checkCallsTotal.WithLabelValues(status).Inc()
}

// RecordCacheHit increments iam_tender_acl_cache_hits_total for TAC-4's
// tac:acl cache (LLD §9/§14.2).
func (m *Metrics) RecordCacheHit(_ context.Context) {
	m.cacheHitsTotal.WithLabelValues(cacheKeyLabel).Inc()
}

// RecordCacheMiss increments iam_tender_acl_cache_misses_total for TAC-4's
// tac:acl cache (LLD §9/§14.2). Not called on a Valkey read error.
func (m *Metrics) RecordCacheMiss(_ context.Context) {
	m.cacheMissesTotal.WithLabelValues(cacheKeyLabel).Inc()
}

// RecordCascade implements consumer.CascadeMetrics: increments
// iam_cascade_operations_total for TenantMembershipsPurged outcomes.
func (m *Metrics) RecordCascade(_ context.Context, result string) {
	m.cascadeOperationsTotal.WithLabelValues(cascadeEventTypeOffboarding, result).Inc()
}

// RecordMemberRemovalCascade implements consumer.MemberRemovalMetrics:
// increments iam_cascade_operations_total for MembershipRevoked outcomes.
// Both cascade consumers share one IAM domain metric; the event_type label
// preserves independent observability and per-consumer alerting.
func (m *Metrics) RecordMemberRemovalCascade(_ context.Context, result string) {
	m.cascadeOperationsTotal.WithLabelValues(cascadeEventTypeMemberRemoval, result).Inc()
}

// RecordUnexpectedEventType implements both consumer.CascadeMetrics and
// consumer.MemberRemovalMetrics: increments iam_unknown_event_acknowledged_total.
// queue is the SQS queue short-name; eventType is the envelope's raw type value.
func (m *Metrics) RecordUnexpectedEventType(_ context.Context, queue, eventType string) {
	m.unknownEventAcknowledgedTotal.WithLabelValues(queue, eventType).Inc()
}

// RecordProcessedEventsDuplicate implements consumer.DuplicateMetrics (IDEMP-4):
// increments iam_tender_acl_processed_events_duplicates_total.
func (m *Metrics) RecordProcessedEventsDuplicate(_ context.Context, consumer string) {
	m.processedEventsDuplicatesTotal.WithLabelValues(consumer).Inc()
}
