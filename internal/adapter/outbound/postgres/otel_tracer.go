package postgres

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// otelTracer adapts the process-global TracerProvider that
// gincommon.InitTracing / ObservabilityMiddlewares install onto
// pgcommon.Config.Tracer (StartSpan). Every db.query span therefore
// exports through the same OTLP pipeline as HTTP spans — matching
// iam-org-membership's and iam-realm-provisioner's postgres.NewOTelTracer.
type otelTracer struct {
	tracer trace.Tracer
}

// NewOTelTracer returns a pgcommon.Config.Tracer backed by gincommon's
// global TracerProvider. serviceName becomes the OTel instrumentation
// scope (typically gincommon.Config.ServiceName).
//
// pgcommon.Config.Tracer is typed as an unexported interface from pgcommon's
// own internal/core/port package, which this module cannot import by name —
// *otelTracer satisfying it structurally is the only way to build a value
// for that field at all.
//
//nolint:revive // unexported-return: see above
func NewOTelTracer(serviceName string) *otelTracer {
	if serviceName == "" {
		serviceName = "tender-acl"
	}
	return &otelTracer{tracer: otel.Tracer(serviceName)}
}

func (o *otelTracer) StartSpan(ctx context.Context, name string) (spanCtx context.Context, end func()) {
	spanCtx, span := o.tracer.Start(ctx, name)
	return spanCtx, func() { span.End() }
}
