package platform

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestTracingOffWithoutEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	stop, err := startTracing(context.Background(), "svc")
	if err != nil {
		t.Fatal(err)
	}
	defer stop(context.Background())
	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); ok {
		t.Error("an exporting provider was set up without an endpoint")
	}
	if otel.GetTextMapPropagator().Fields() == nil {
		t.Error("no propagator: incoming traces would not continue")
	}
}

func TestTracingOnWithEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	stop, err := startTracing(context.Background(), "svc")
	if err != nil {
		t.Fatal(err)
	}
	provider, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider)
	if !ok {
		t.Fatal("no exporting provider with an endpoint")
	}
	_ = stop(context.Background())
	otel.SetTracerProvider(sdktrace.NewTracerProvider()) // leave a clean global for other tests
	_ = provider
}
