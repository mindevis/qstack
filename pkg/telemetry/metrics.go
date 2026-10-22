package telemetry

import (
	"context"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// NewRegistry returns a Prometheus registry with Go runtime and process metrics.
func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return reg
}

// GRPCMetrics records RED metrics of a gRPC server (REQ-OBS-001).
//
// Put its interceptors first in the chain (outermost) so that the recorded
// code is the final one, after apierr has sanitized the error.
type GRPCMetrics struct {
	handled *prometheus.CounterVec
	seconds *prometheus.HistogramVec
}

// NewGRPCMetrics registers the gRPC server metrics in reg.
func NewGRPCMetrics(reg prometheus.Registerer) *GRPCMetrics {
	m := &GRPCMetrics{
		handled: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "qs_grpc_server_handled_total",
			Help: "Completed gRPC calls by service, method and status code.",
		}, []string{"grpc_service", "grpc_method", "grpc_code"}),
		seconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "qs_grpc_server_handling_seconds",
			Help:    "gRPC call latency until the handler returns.",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
		}, []string{"grpc_service", "grpc_method"}),
	}
	reg.MustRegister(m.handled, m.seconds)
	return m
}

// UnaryServerInterceptor records unary calls.
func (m *GRPCMetrics) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now() //nolint:forbidigo // latency is measured in real time, not scaled business time
		resp, err := handler(ctx, req)
		m.observe(info.FullMethod, err, start)
		return resp, err
	}
}

// StreamServerInterceptor records streaming calls (latency = stream lifetime).
func (m *GRPCMetrics) StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now() //nolint:forbidigo // latency is measured in real time
		err := handler(srv, ss)
		m.observe(info.FullMethod, err, start)
		return err
	}
}

func (m *GRPCMetrics) observe(fullMethod string, err error, start time.Time) {
	svc, method := splitMethod(fullMethod)
	m.handled.WithLabelValues(svc, method, status.Code(err).String()).Inc()
	m.seconds.WithLabelValues(svc, method).Observe(time.Since(start).Seconds()) //nolint:forbidigo // real-time latency
}

// splitMethod turns "/pkg.Service/Method" into ("pkg.Service", "Method").
func splitMethod(full string) (string, string) {
	full = strings.TrimPrefix(full, "/")
	if i := strings.LastIndex(full, "/"); i >= 0 {
		return full[:i], full[i+1:]
	}
	return "unknown", full
}
