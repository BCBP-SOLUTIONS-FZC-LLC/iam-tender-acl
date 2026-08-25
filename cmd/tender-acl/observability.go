package main

import (
	"context"

	"go.opentelemetry.io/otel/trace"
)

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
