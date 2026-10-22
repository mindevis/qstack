package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
)

func TestSetupTracingWithoutExporter(t *testing.T) {
	shutdown, err := SetupTracing(context.Background(), TracingOptions{Service: "qs-test", Version: "dev", SampleRatio: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, span := otel.Tracer("test").Start(context.Background(), "op")
	if !span.SpanContext().IsValid() || !span.SpanContext().IsSampled() {
		t.Errorf("span context = %+v, want valid and sampled", span.SpanContext())
	}
	span.End()
	if err := shutdown(context.Background()); err != nil {
		t.Errorf("shutdown: %v", err)
	}
}

func TestSetupTracingSamplesNothingAtZeroRatio(t *testing.T) {
	shutdown, err := SetupTracing(context.Background(), TracingOptions{Service: "qs-test", SampleRatio: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = shutdown(context.Background()) }()
	_, span := otel.Tracer("test").Start(context.Background(), "op")
	defer span.End()
	if span.SpanContext().IsSampled() {
		t.Error("root span sampled at ratio 0")
	}
}

func TestSetupTracingWithEndpoint(t *testing.T) {
	// The exporter connects lazily, so setup succeeds without a collector.
	shutdown, err := SetupTracing(context.Background(), TracingOptions{
		Service: "qs-test", Endpoint: "127.0.0.1:1", Insecure: true, SampleRatio: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // do not wait for the unreachable collector on shutdown
	_ = shutdown(ctx)
}
