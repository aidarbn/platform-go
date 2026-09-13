package platform

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
)

// startTracing sets up OpenTelemetry as taply does: spans go over OTLP gRPC to the
// collector of the monitoring agent, and trace context travels with W3C traceparent and
// baggage. Without OTEL_EXPORTER_OTLP_ENDPOINT nothing is exported, yet an incoming trace
// is still continued, so a service in the middle of a call chain does not break it.
// The exporter and the sampler read the standard OTEL_* variables themselves.
func startTracing(ctx context.Context, service string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))

	if strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")) == "" &&
		strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")) == "" {
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("tracing: exporter: %w", err)
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(semconv.ServiceName(service)))
	if err != nil {
		return nil, fmt.Errorf("tracing: resource: %w", err)
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}
