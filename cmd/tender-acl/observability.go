package main

import (
	"context"
	"log/slog"

	domain "github.com/BCBP-SOLUTIONS-FZC-LLC/platform-pgcommon/pkg/domain"
	"go.opentelemetry.io/otel/trace"
)

// slogDomainLogger adapts *slog.Logger to platform-pgcommon's domain.Logger
// interface (Field-based — a different shape from platform-gincommon's own
// map[string]interface{}-based Logger, see slogPlatformLogger in
// internal/adapter/inbound/http/router.go). Wired into pgcommon.Config.Logger
// so slow-query WARN log lines flow through the same structured logger as
// everything else in this process, instead of going nowhere — Config.Logger
// was previously left unset entirely.
type slogDomainLogger struct{ l *slog.Logger }

func (a slogDomainLogger) Debug(msg string, fields ...domain.Field) {
	a.l.Debug(msg, domainFieldsToArgs(fields)...)
}
func (a slogDomainLogger) Info(msg string, fields ...domain.Field) {
	a.l.Info(msg, domainFieldsToArgs(fields)...)
}
func (a slogDomainLogger) Warn(msg string, fields ...domain.Field) {
	a.l.Warn(msg, domainFieldsToArgs(fields)...)
}
func (a slogDomainLogger) Error(msg string, fields ...domain.Field) {
	a.l.Error(msg, domainFieldsToArgs(fields)...)
}

func domainFieldsToArgs(fields []domain.Field) []any {
	args := make([]any, 0, len(fields)*2)
	for _, f := range fields {
		args = append(args, f.Key, f.Value)
	}
	return args
}

// otelSpanTracer adapts an OTel trace.Tracer to the structural
// StartSpan(ctx, name) (context.Context, func()) shape pgcommon.Config.Tracer
// expects (satisfied here without importing pgcommon's internal port
// package — Go interface satisfaction is structural). Wired in so every
// Postgres query gets its own child "db.query" OTel span under the
// request's trace (ARCHITECTURE.md's outbound.postgres hop) — Config.Tracer
// was previously left unset entirely, so no such spans existed.
type otelSpanTracer struct{ tracer trace.Tracer }

func (t otelSpanTracer) StartSpan(ctx context.Context, name string) (spanCtx context.Context, end func()) {
	spanCtx, span := t.tracer.Start(ctx, name)
	return spanCtx, func() { span.End() }
}
