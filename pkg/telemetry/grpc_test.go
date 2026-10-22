package telemetry

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"

	"github.com/mindevis/qstack/pkg/apierr"
)

type failingHealth struct {
	healthpb.UnimplementedHealthServer
	err atomic.Pointer[error] // set by the test, read by the handler goroutine
}

func (f *failingHealth) setErr(err error) { f.err.Store(&err) }

func (f *failingHealth) Check(context.Context, *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	if p := f.err.Load(); p != nil && *p != nil {
		return nil, *p
	}
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

func dial(t *testing.T, srv *grpc.Server) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// REQ-OBS-001: RED metrics per method and final status code.
func TestGRPCMetricsRecordFinalCode(t *testing.T) {
	reg := NewRegistry()
	m := NewGRPCMetrics(reg)
	impl := &failingHealth{}
	srv := grpc.NewServer(
		// metrics outermost, so it sees the code after apierr sanitized it
		grpc.ChainUnaryInterceptor(m.UnaryServerInterceptor(), apierr.UnaryServerInterceptor(apierr.Options{})),
	)
	healthpb.RegisterHealthServer(srv, impl)
	client := healthpb.NewHealthClient(dial(t, srv))

	ctx := context.Background()
	_, _ = client.Check(ctx, &healthpb.HealthCheckRequest{})
	_, _ = client.Check(ctx, &healthpb.HealthCheckRequest{})
	impl.setErr(errors.New("raw failure")) // sanitized to INTERNAL
	_, _ = client.Check(ctx, &healthpb.HealthCheckRequest{})
	impl.setErr(apierr.NotFound("instance", "ins-x"))
	_, _ = client.Check(ctx, &healthpb.HealthCheckRequest{})

	// Count before the checks below: WithLabelValues would create empty series.
	if n, err := testutil.GatherAndCount(reg, "qs_grpc_server_handled_total"); err != nil || n != 3 {
		t.Errorf("registry exposes %d handled series (err %v), want 3", n, err)
	}
	const svc, method = "grpc.health.v1.Health", "Check"
	for code, want := range map[codes.Code]float64{codes.OK: 2, codes.Internal: 1, codes.NotFound: 1, codes.Unknown: 0} {
		got := testutil.ToFloat64(m.handled.WithLabelValues(svc, method, code.String()))
		if got != want {
			t.Errorf("handled{code=%s} = %v, want %v", code, got, want)
		}
	}
	if n := testutil.CollectAndCount(m.seconds); n != 1 {
		t.Errorf("histogram series = %d, want 1", n)
	}
}

func TestSplitMethod(t *testing.T) {
	tests := map[string][2]string{
		"/qstack.operation.v1.OperationService/GetOperation": {"qstack.operation.v1.OperationService", "GetOperation"},
		"nomethod": {"unknown", "nomethod"},
	}
	for in, want := range tests {
		if s, m := splitMethod(in); s != want[0] || m != want[1] {
			t.Errorf("splitMethod(%q) = %q, %q", in, s, m)
		}
	}
}

// Spec 02 §13: gRPC Health Checking Protocol follows readiness.
func TestSyncGRPC(t *testing.T) {
	h := NewHealth(time.Second)
	var failing atomic.Bool
	h.AddCheck("db", func(context.Context) error {
		if failing.Load() {
			return errors.New("db down")
		}
		return nil
	})

	hs := health.NewServer()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.SyncGRPC(ctx, hs, 10*time.Millisecond); close(done) }()

	waitStatus(t, hs, healthpb.HealthCheckResponse_SERVING)
	failing.Store(true)
	waitStatus(t, hs, healthpb.HealthCheckResponse_NOT_SERVING)

	failing.Store(false)
	cancel()
	<-done
	// After shutdown the server stays NOT_SERVING even though checks pass again.
	resp, err := hs.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil || resp.GetStatus() != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Errorf("after shutdown: %v, %v; want NOT_SERVING", resp.GetStatus(), err)
	}
}

func waitStatus(t *testing.T, hs *health.Server, want healthpb.HealthCheckResponse_ServingStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second) //nolint:forbidigo // test timeout in real time
	for time.Now().Before(deadline) {           //nolint:forbidigo // test timeout in real time
		resp, err := hs.Check(context.Background(), &healthpb.HealthCheckRequest{})
		if err == nil && resp.GetStatus() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("gRPC health never became %v", want)
}
