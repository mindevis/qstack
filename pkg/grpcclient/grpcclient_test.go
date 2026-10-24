package grpcclient_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/resolver/manual"
	"google.golang.org/grpc/status"

	operationv1 "github.com/mindevis/qstack/gen/go/qstack/operation/v1"
	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/authz"
	"github.com/mindevis/qstack/pkg/grpcclient"
)

// backend answers GetOperation with its name and records what it saw.
type backend struct {
	operationv1.UnimplementedOperationServiceServer
	name   string
	health *health.Server
	addr   string

	mu       sync.Mutex
	calls    int
	failNext int // answer UNAVAILABLE this many times
	md       metadata.MD
	deadline time.Duration
}

func (b *backend) GetOperation(ctx context.Context, _ *operationv1.GetOperationRequest) (*operationv1.Operation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	b.md, _ = metadata.FromIncomingContext(ctx)
	if d, ok := ctx.Deadline(); ok {
		b.deadline = time.Until(d) //nolint:forbidigo // measuring a gRPC deadline needs real time
	}
	if b.failNext > 0 {
		b.failNext--
		return nil, status.Error(codes.Unavailable, "restarting")
	}
	return &operationv1.Operation{Id: b.name}, nil
}

func (b *backend) failFor(n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failNext = n
}

func (b *backend) seen() (metadata.MD, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.md, b.deadline
}

func (b *backend) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

func startBackend(t *testing.T, name string) *backend {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &backend{name: name, health: health.NewServer(), addr: lis.Addr().String()}
	srv := grpc.NewServer()
	operationv1.RegisterOperationServiceServer(srv, b)
	healthpb.RegisterHealthServer(srv, b.health)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return b
}

func dial(t *testing.T, backends ...*backend) operationv1.OperationServiceClient {
	t.Helper()
	r := manual.NewBuilderWithScheme("test")
	var addrs []resolver.Address
	for _, b := range backends {
		addrs = append(addrs, resolver.Address{Addr: b.addr})
	}
	r.InitialState(resolver.State{Addresses: addrs})
	conn, err := grpcclient.New("test:///svc", grpcclient.Options{Insecure: true, DialOptions: []grpc.DialOption{grpc.WithResolvers(r)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return operationv1.NewOperationServiceClient(conn)
}

func get(ctx context.Context, c operationv1.OperationServiceClient) (string, error) {
	op, err := c.GetOperation(ctx, &operationv1.GetOperationRequest{OperationId: "op-x"})
	return op.GetId(), err
}

// Spec 01 §7: calls are spread over all healthy backends.
func TestRoundRobinOverHealthyBackends(t *testing.T) {
	a, b := startBackend(t, "a"), startBackend(t, "b")
	c := dial(t, a, b)
	for range 20 {
		if _, err := get(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	if a.count() < 5 || b.count() < 5 {
		t.Errorf("calls a=%d b=%d, want both used", a.count(), b.count())
	}

	b.health.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	time.Sleep(200 * time.Millisecond) // the health watch reaches the client
	before := b.count()
	for range 10 {
		if name, err := get(context.Background(), c); err != nil || name != "a" {
			t.Fatalf("with b unhealthy: %q, %v", name, err)
		}
	}
	if b.count() != before {
		t.Error("unhealthy backend still received calls")
	}
}

// Spec 02 §11: UNAVAILABLE is retried at most 3 times; the default
// deadline is 5 s.
func TestRetryAndTimeout(t *testing.T) {
	a := startBackend(t, "a")
	c := dial(t, a)
	a.failFor(3)
	if _, err := get(context.Background(), c); err != nil {
		t.Fatalf("3 transient failures not retried: %v", err)
	}
	if a.count() != 4 {
		t.Errorf("%d attempts, want 4", a.count())
	}
	a.failFor(4)
	if _, err := get(context.Background(), c); status.Code(err) != codes.Unavailable {
		t.Errorf("after 4 failures: %v", err)
	}
	if _, d := a.seen(); d <= 4*time.Second || d > 5*time.Second {
		t.Errorf("deadline %v, want ~5s", d)
	}
}

// Spec 02 §11: request ID and end-user subject travel with the call.
func TestPropagatesCallerContext(t *testing.T) {
	a := startBackend(t, "a")
	c := dial(t, a)
	ctx := apierr.WithRequestID(context.Background(), "req-123")
	ctx = authz.WithSubject(ctx, authz.Subject{ID: "usr-1", Type: "user"})
	if _, err := get(ctx, c); err != nil {
		t.Fatal(err)
	}
	md, _ := a.seen()
	if got := md.Get(apierr.RequestIDHeader); len(got) != 1 || got[0] != "req-123" {
		t.Errorf("request id %v", got)
	}
	if got := md.Get(grpcclient.SubjectHeader); len(got) != 1 || got[0] != "usr-1" {
		t.Errorf("subject %v", got)
	}
	if _, err := grpcclient.New("dns:///x:1", grpcclient.Options{}); err == nil {
		t.Error("dial without TLS accepted")
	}
}
