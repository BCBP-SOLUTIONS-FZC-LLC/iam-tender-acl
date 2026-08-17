// Package metrics registers every OpenTelemetry instrument this service
// emits, named per tender-acl-service-lld.md §14.2.
package metrics

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metrics holds every OpenTelemetry instrument this service emits.
type Metrics struct {
	requestsTotal             metric.Int64Counter
	requestDuration           metric.Float64Histogram
	writesTotal               metric.Int64Counter
	grantChecksTotal          metric.Int64Counter
	checkCallsTotal           metric.Int64Counter
	cascadeTotal              metric.Int64Counter
	memberRemovalCascadeTotal metric.Int64Counter
}

// NewMetrics registers every tender_acl_* instrument against meter.
func NewMetrics(meter metric.Meter) (*Metrics, error) {
	requestsTotal, err := meter.Int64Counter(
		"tender_acl_requests_total",
		metric.WithDescription("Total HTTP requests handled, by route/status"),
	)
	if err != nil {
		return nil, err
	}

	requestDuration, err := meter.Float64Histogram(
		"tender_acl_request_duration_seconds",
		metric.WithDescription("HTTP request duration in seconds, by route"),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, err
	}

	writesTotal, err := meter.Int64Counter(
		"tender_acl_writes_total",
		metric.WithDescription("Total grant/revoke writes, by op and result"),
	)
	if err != nil {
		return nil, err
	}

	grantChecksTotal, err := meter.Int64Counter(
		"tender_acl_grant_checks_total",
		metric.WithDescription("Total TAC-2 grant-time membership checks, by status (active/not_active/unavailable)"),
	)
	if err != nil {
		return nil, err
	}

	checkCallsTotal, err := meter.Int64Counter(
		"tender_acl_check_calls_total",
		metric.WithDescription("Total TAC-4 access checks, by status (has_access/no_access)"),
	)
	if err != nil {
		return nil, err
	}

	cascadeTotal, err := meter.Int64Counter(
		"tender_acl_tenant_offboarding_cascade_total",
		metric.WithDescription("Total tenant offboarding cascade deletions, by result"),
	)
	if err != nil {
		return nil, err
	}

	// ADR-0007 Wave 3 Phase 3 (O_AND_M_DELTA.md §5 Option B) — a separate
	// counter from cascadeTotal above, not a shared metric with an extra
	// label: the tenant-offboarding and per-user-removal cascades are
	// different failure domains (different queues, different DLQs,
	// different alert runbooks) and conflating them into one series would
	// make each individually harder to reason about during an incident.
	memberRemovalCascadeTotal, err := meter.Int64Counter(
		"tender_acl_member_removal_cascade_total",
		metric.WithDescription("Total per-user-removal ACL cascade soft-deletes, by result"),
	)
	if err != nil {
		return nil, err
	}

	return &Metrics{
		requestsTotal:             requestsTotal,
		requestDuration:           requestDuration,
		writesTotal:               writesTotal,
		grantChecksTotal:          grantChecksTotal,
		checkCallsTotal:           checkCallsTotal,
		cascadeTotal:              cascadeTotal,
		memberRemovalCascadeTotal: memberRemovalCascadeTotal,
	}, nil
}

// RecordRequest increments tender_acl_requests_total, tagged by method/route/status.
func (m *Metrics) RecordRequest(ctx context.Context, method, route string, status int) {
	m.requestsTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("method", method),
		attribute.String("path", route),
		attribute.Int("status", status),
	))
}

// RecordRequestDuration records tender_acl_request_duration_seconds, tagged by method/route.
func (m *Metrics) RecordRequestDuration(ctx context.Context, method, route string, seconds float64) {
	m.requestDuration.Record(ctx, seconds, metric.WithAttributes(
		attribute.String("method", method),
		attribute.String("path", route),
	))
}

// RecordWrite increments tender_acl_writes_total for a grant/revoke op, tagged by result.
func (m *Metrics) RecordWrite(ctx context.Context, op, result string) {
	m.writesTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("op", op),
		attribute.String("result", result),
	))
}

// RecordGrantCheck increments tender_acl_grant_checks_total, tagged by status
// (active/not_active/unavailable).
func (m *Metrics) RecordGrantCheck(ctx context.Context, status string) {
	m.grantChecksTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("status", status)))
}

// RecordCheckCall increments tender_acl_check_calls_total, tagged by status
// (has_access/no_access).
func (m *Metrics) RecordCheckCall(ctx context.Context, status string) {
	m.checkCallsTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("status", status)))
}

// RecordCascade implements consumer.CascadeMetrics, so main.go can pass a
// *Metrics directly to the offboarding consumer.
func (m *Metrics) RecordCascade(ctx context.Context, result string) {
	m.cascadeTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

// RecordMemberRemovalCascade implements consumer.MemberRemovalMetrics, so
// main.go can pass a *Metrics directly to the member removal consumer.
func (m *Metrics) RecordMemberRemovalCascade(ctx context.Context, result string) {
	m.memberRemovalCascadeTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}
