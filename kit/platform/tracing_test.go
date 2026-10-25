package platform

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
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

// OTEL_SERVICE_NAME names the service in the spans, over the service of the project, so a
// service moving to the platform keeps the name its traces are searched by.
func TestTracingServiceName(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	for env, want := range map[string]string{"": "svc", "be-maas-api": "be-maas-api"} {
		t.Setenv("OTEL_SERVICE_NAME", env)
		stop, err := startTracing(context.Background(), "svc")
		if err != nil {
			t.Fatal(err)
		}
		provider := otel.GetTracerProvider().(*sdktrace.TracerProvider)
		rec := tracetest.NewSpanRecorder()
		provider.RegisterSpanProcessor(rec)
		_, span := provider.Tracer("test").Start(context.Background(), "call")
		span.End()
		got := ""
		for _, kv := range rec.Ended()[0].Resource().Attributes() {
			if kv.Key == semconv.ServiceNameKey {
				got = kv.Value.AsString()
			}
		}
		if got != want {
			t.Errorf("OTEL_SERVICE_NAME=%q: service.name = %q, want %q", env, got, want)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_ = stop(ctx)
		cancel()
	}
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
}
