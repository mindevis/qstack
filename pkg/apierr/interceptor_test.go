package apierr

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// healthServer lets each test decide what the handler does.
type healthServer struct {
	healthpb.UnimplementedHealthServer
	check func(ctx context.Context) error
	watch func(stream grpc.ServerStream) error
}

func (h *healthServer) Check(ctx context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	if err := h.check(ctx); err != nil {
		return nil, err
	}
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

func (h *healthServer) Watch(_ *healthpb.HealthCheckRequest, s grpc.ServerStreamingServer[healthpb.HealthCheckResponse]) error {
	return h.watch(s)
}

type internalLog struct {
	mu     sync.Mutex
	causes []string
	reqIDs []string
}

func (l *internalLog) record(ctx context.Context, cause error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.causes = append(l.causes, cause.Error())
	l.reqIDs = append(l.reqIDs, RequestID(ctx))
}

func startServer(t *testing.T, h *healthServer, log *internalLog) healthpb.HealthClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	opts := Options{OnInternal: log.record}
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(UnaryServerInterceptor(opts)),
		grpc.StreamInterceptor(StreamServerInterceptor(opts)),
	)
	healthpb.RegisterHealthServer(srv, h)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return healthpb.NewHealthClient(conn)
}

// REQ-PLT-060: internal details never reach the client; the cause is logged.
func TestUnaryHidesInternalDetails(t *testing.T) {
	tests := []struct {
		name  string
		check func(context.Context) error
		cause string
	}{
		{"plain error", func(context.Context) error {
			return errors.New("pq: relation \"instances\" does not exist on db-7.internal")
		}, "pq: relation"},
		{"unknown status", func(context.Context) error { return status.Error(codes.Unknown, "boom at host-12") }, "boom at host-12"},
		{"internal status", func(context.Context) error { return status.Error(codes.Internal, "nil map in reconcile.go:42") }, "reconcile.go:42"},
		{"panic", func(context.Context) error { panic("index out of range in store.go") }, "panic: index out of range"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := &internalLog{}
			client := startServer(t, &healthServer{check: tt.check}, log)

			var hdr metadata.MD
			_, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{}, grpc.Header(&hdr))

			st := status.Convert(err)
			if st.Code() != codes.Internal || st.Message() != "internal error" {
				t.Fatalf("got %v %q, want INTERNAL \"internal error\"", st.Code(), st.Message())
			}
			reqID := requestInfoID(st)
			if reqID == "" || reqID != first(hdr.Get(RequestIDHeader)) {
				t.Errorf("RequestInfo.request_id = %q, header = %v; want equal and non-empty", reqID, hdr.Get(RequestIDHeader))
			}
			if len(log.causes) != 1 || !strings.Contains(log.causes[0], tt.cause) || log.reqIDs[0] != reqID {
				t.Errorf("OnInternal got causes=%v reqIDs=%v; want the original cause with request ID %s", log.causes, log.reqIDs, reqID)
			}
		})
	}
}

func TestUnaryPassesQStackErrors(t *testing.T) {
	log := &internalLog{}
	want := NotFound("instance", "ins-x")
	client := startServer(t, &healthServer{check: func(context.Context) error { return want }}, log)

	_, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if status.Code(err) != codes.NotFound || !IsReason(err, Reason(want)) {
		t.Errorf("got %v, want the NotFound error unchanged", err)
	}
	if len(log.causes) != 0 {
		t.Errorf("OnInternal called for a regular error: %v", log.causes)
	}
}

// REQ-PLT-061: a valid client request ID is reused, an invalid one replaced.
func TestRequestIDPropagation(t *testing.T) {
	tests := []struct {
		name     string
		incoming string
		reuse    bool
	}{
		{"valid id is reused", "client-abc_123.x", true},
		{"none generates", "", false},
		{"too long is replaced", strings.Repeat("a", 65), false},
		// The gRPC client itself refuses control characters, so test printable
		// characters outside the allowed set.
		{"unsafe chars are replaced", "abc def;<script>", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen string
			client := startServer(t, &healthServer{check: func(ctx context.Context) error {
				seen = RequestID(ctx)
				return nil
			}}, &internalLog{})

			ctx := context.Background()
			if tt.incoming != "" {
				ctx = metadata.AppendToOutgoingContext(ctx, RequestIDHeader, tt.incoming)
			}
			var hdr metadata.MD
			if _, err := client.Check(ctx, &healthpb.HealthCheckRequest{}, grpc.Header(&hdr)); err != nil {
				t.Fatalf("Check: %v", err)
			}
			got := first(hdr.Get(RequestIDHeader))
			if got == "" || got != seen {
				t.Fatalf("header %q, handler saw %q; want equal and non-empty", got, seen)
			}
			if tt.reuse != (got == tt.incoming) {
				t.Errorf("request ID = %q, incoming %q, reuse=%v", got, tt.incoming, tt.reuse)
			}
		})
	}
}

func TestStreamInterceptor(t *testing.T) {
	log := &internalLog{}
	var seen string
	client := startServer(t, &healthServer{watch: func(s grpc.ServerStream) error {
		seen = RequestID(s.Context())
		panic("stream handler bug")
	}}, log)

	stream, err := client.Watch(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	_, err = stream.Recv()
	st := status.Convert(err)
	if st.Code() != codes.Internal || st.Message() != "internal error" {
		t.Fatalf("got %v %q, want sanitized INTERNAL", st.Code(), st.Message())
	}
	if seen == "" || requestInfoID(st) != seen {
		t.Errorf("handler request ID %q, RequestInfo %q", seen, requestInfoID(st))
	}
	if len(log.causes) != 1 {
		t.Errorf("OnInternal causes = %v", log.causes)
	}
}

func TestContextErrorsKeepTheirCode(t *testing.T) {
	client := startServer(t, &healthServer{check: func(context.Context) error { return context.DeadlineExceeded }}, &internalLog{})
	_, err := client.Check(context.Background(), &healthpb.HealthCheckRequest{})
	if status.Code(err) != codes.DeadlineExceeded {
		t.Errorf("code = %v, want DeadlineExceeded", status.Code(err))
	}
}

func requestInfoID(st *status.Status) string {
	for _, d := range st.Details() {
		if ri, ok := d.(*errdetails.RequestInfo); ok {
			return ri.GetRequestId()
		}
	}
	return ""
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}
