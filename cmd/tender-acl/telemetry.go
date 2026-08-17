package main

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const serviceName = "tender-acl"

// telemetry owns every OpenTelemetry provider this process creates, so main
// can shut them down cleanly on exit.
type telemetry struct {
	TracerProvider *sdktrace.TracerProvider
	MeterProvider  *sdkmetric.MeterProvider
	Tracer         trace.Tracer
	Meter          metric.Meter
}

// setupTelemetry wires the OpenTelemetry SDK: traces are batched and
// exported over OTLP/HTTP (async, non-blocking on export failures — a
// missing collector never fails a request), and metrics are exposed via the
// Prometheus exporter for cmd/tender-acl's own /metrics endpoint.
func setupTelemetry(ctx context.Context, cfg config) (*telemetry, error) {
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		semconv.ServiceName(serviceName),
		semconv.DeploymentEnvironment(cfg.Environment),
	))
	if err != nil {
		return nil, fmt.Errorf("build otel resource: %w", err)
	}

	traceExporter, err := otlptracehttp.New(ctx, otlptracehttp.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("build otlp trace exporter: %w", err)
	}
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tracerProvider)

	promExporter, err := prometheus.New()
	if err != nil {
		return nil, fmt.Errorf("build prometheus exporter: %w", err)
	}
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(promExporter),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(meterProvider)

	return &telemetry{
		TracerProvider: tracerProvider,
		MeterProvider:  meterProvider,
		Tracer:         tracerProvider.Tracer(serviceName),
		Meter:          meterProvider.Meter(serviceName),
	}, nil
}

func (t *telemetry) Shutdown(ctx context.Context) {
	if err := t.TracerProvider.Shutdown(ctx); err != nil {
		slog.Error("tracer provider shutdown failed", slog.String("error", err.Error()))
	}
	if err := t.MeterProvider.Shutdown(ctx); err != nil {
		slog.Error("meter provider shutdown failed", slog.String("error", err.Error()))
	}
}
