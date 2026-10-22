package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc"
)

// TracingOptions configures SetupTracing.
type TracingOptions struct {
	Service string
	Version string
	// Endpoint is the OTLP/gRPC collector (host:port); empty records spans
	// locally (trace IDs still appear in logs) but exports nothing.
	Endpoint string
	// Insecure disables TLS to the collector.
	Insecure bool
	// SampleRatio applies to new root traces; children follow their parent.
	SampleRatio float64
}

// SetupTracing installs the global TracerProvider and W3C trace-context
// propagation. Call the returned function on shutdown to flush spans.
func SetupTracing(ctx context.Context, o TracingOptions) (func(context.Context) error, error) {
	res := resource.NewSchemaless(
		attribute.String("service.name", o.Service),
		attribute.String("service.version", o.Version),
	)
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(o.SampleRatio))),
	}
	if o.Endpoint != "" {
		expOpts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(o.Endpoint)}
		if o.Insecure {
			expOpts = append(expOpts, otlptracegrpc.WithInsecure())
		}
		exp, err := otlptracegrpc.New(ctx, expOpts...)
		if err != nil {
			return nil, fmt.Errorf("telemetry: OTLP exporter: %w", err)
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	}
	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return tp.Shutdown, nil
}

// GRPCServerTracing returns the server option that creates a span per call
// and continues traces from incoming `traceparent` metadata.
func GRPCServerTracing() grpc.ServerOption {
	return grpc.StatsHandler(otelgrpc.NewServerHandler())
}

// GRPCClientTracing returns the dial option that propagates the trace to callees.
func GRPCClientTracing() grpc.DialOption {
	return grpc.WithStatsHandler(otelgrpc.NewClientHandler())
}
