package authz_test

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	apipb "github.com/mindevis/qstack/gen/go/qstack/api"
	iaminternalv1 "github.com/mindevis/qstack/gen/go/qstack/iam/internalv1"
	operationv1 "github.com/mindevis/qstack/gen/go/qstack/operation/v1"
	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/authz"
	"github.com/mindevis/qstack/pkg/authz/authzfake"
	"github.com/mindevis/qstack/pkg/clock"
)

const (
	org      = "org-01m5hqpzg00000000000000000"
	project  = "prj-01m5hqpzg00000000000000000"
	other    = "prj-01m5hqpzg00000000000000001"
	opID     = "op-01m5hqpzg00000000000000000"
	alice    = "usr-01m5hqpzg00000000000000000"
	getPerm  = "operations.operations.get"
	canPerm  = "operations.operations.cancel"
	listPerm = "operations.operations.list"
)

var logger = slog.New(slog.DiscardHandler)

// opServer is a minimal OperationService whose handlers authorize like a
// real service; behaviour can be swapped per test.
type opServer struct {
	operationv1.UnimplementedOperationServiceServer
	cancel func(ctx context.Context) error
}

// The operation lives in `project`.
var opPath = authz.ProjectPath(org, project).Child("operation", opID)

func (s *opServer) GetOperation(ctx context.Context, req *operationv1.GetOperationRequest) (*operationv1.Operation, error) {
	if req.GetOperationId() != opID {
		return nil, apierr.NotFound("operation", req.GetOperationId()) // before any check: allowed
	}
	if err := authz.CheckResource(ctx, opPath, getPerm); err != nil {
		return nil, err
	}
	return &operationv1.Operation{Id: opID}, nil
}

func (s *opServer) ListOperations(ctx context.Context, req *operationv1.ListOperationsRequest) (*operationv1.ListOperationsResponse, error) {
	if err := authz.Check(ctx, authz.ProjectPath(org, req.GetProjectId())); err != nil {
		return nil, err
	}
	return &operationv1.ListOperationsResponse{}, nil
}

func (s *opServer) CancelOperation(ctx context.Context, _ *operationv1.CancelOperationRequest) (*operationv1.Operation, error) {
	if s.cancel != nil {
		if err := s.cancel(ctx); err != nil {
			return nil, err
		}
		return &operationv1.Operation{Id: opID}, nil
	}
	if err := authz.CheckResource(ctx, opPath, getPerm); err != nil {
		return nil, err
	}
	return &operationv1.Operation{Id: opID, Done: true}, nil
}

// subjectFromMetadata stands in for authentication: x-test-subject → Subject.
func subjectFromMetadata(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("x-test-subject"); len(v) == 1 {
		ctx = authz.WithSubject(ctx, authz.Subject{ID: v[0], Type: "user"})
	}
	return h(ctx, req)
}

type env struct {
	access *authzfake.AccessService
	ops    *opServer
	client operationv1.OperationServiceClient
	health healthpb.HealthClient
	srv    *grpc.Server
}

var public = map[string]bool{"/grpc.health.v1.Health/Check": true}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{access: authzfake.New(), ops: &opServer{}}
	opts := authz.Options{Authorizer: e.access, Logger: logger, Public: public}
	e.srv = grpc.NewServer(grpc.ChainUnaryInterceptor(subjectFromMetadata, authz.UnaryServerInterceptor(opts)),
		grpc.ChainStreamInterceptor(authz.StreamServerInterceptor(opts)))
	operationv1.RegisterOperationServiceServer(e.srv, e.ops)
	healthpb.RegisterHealthServer(e.srv, health.NewServer())
	conn := serve(t, e.srv)
	e.client = operationv1.NewOperationServiceClient(conn)
	e.health = healthpb.NewHealthClient(conn)
	return e
}

func serve(t *testing.T, srv *grpc.Server) *grpc.ClientConn {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func as(subject string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "x-test-subject", subject)
}

func code(err error) codes.Code { return status.Code(err) }

func TestUnauthenticated(t *testing.T) {
	e := newEnv(t)
	_, err := e.client.GetOperation(context.Background(), &operationv1.GetOperationRequest{OperationId: opID})
	if code(err) != codes.Unauthenticated {
		t.Errorf("err = %v, want UNAUTHENTICATED", err)
	}
}

// Spec 03 §9.2 / REQ-IAM-011: granted → OK; no access at all → NOT_FOUND
// (existence not revealed); may read but not act → PERMISSION_DENIED.
func TestResourceChecks(t *testing.T) {
	e := newEnv(t)
	ctx := as(alice)
	get := func() error {
		_, err := e.client.GetOperation(ctx, &operationv1.GetOperationRequest{OperationId: opID})
		return err
	}
	cancel := func() error {
		_, err := e.client.CancelOperation(ctx, &operationv1.CancelOperationRequest{OperationId: opID})
		return err
	}
	if err := get(); code(err) != codes.NotFound {
		t.Errorf("get without access: %v, want NOT_FOUND", err)
	}
	if err := cancel(); code(err) != codes.NotFound {
		t.Errorf("cancel without access: %v, want NOT_FOUND", err)
	}

	e.access.Grant(alice, getPerm, authz.Resource{Type: "project", ID: project})
	if err := get(); err != nil {
		t.Errorf("get with project grant: %v", err)
	}
	if err := cancel(); code(err) != codes.PermissionDenied || !apierr.IsReason(err, apipb.ErrorReason_QS_PERMISSION_DENIED) {
		t.Errorf("cancel with get only: %v, want PERMISSION_DENIED", err)
	}
	// Spec 03 §9.3: a role on the organization covers its projects.
	e.access.Grant(alice, canPerm, authz.Resource{Type: "organization", ID: org})
	if err := cancel(); err != nil {
		t.Errorf("cancel with organization grant: %v", err)
	}
}

// REQ-IAM-012: List needs <svc>.<res>.list on the project.
func TestListOnProject(t *testing.T) {
	e := newEnv(t)
	e.access.Grant(alice, listPerm, authz.Resource{Type: "project", ID: project})
	list := func(p string) error {
		_, err := e.client.ListOperations(as(alice), &operationv1.ListOperationsRequest{
			Scope: &operationv1.ListOperationsRequest_ProjectId{ProjectId: p}})
		return err
	}
	if err := list(project); err != nil {
		t.Errorf("list own project: %v", err)
	}
	if err := list(other); code(err) != codes.PermissionDenied {
		t.Errorf("list other project: %v, want PERMISSION_DENIED", err)
	}
}

// Fail closed: a handler that skips or ignores the check never returns its
// response; only INVALID_ARGUMENT / NOT_FOUND may precede the check.
func TestHandlerMustAuthorize(t *testing.T) {
	e := newEnv(t)
	ctx := as(alice)
	call := func() error {
		_, err := e.client.CancelOperation(ctx, &operationv1.CancelOperationRequest{OperationId: opID})
		return err
	}
	for name, tc := range map[string]struct {
		handler func(context.Context) error
		want    codes.Code
	}{
		"no check":         {func(context.Context) error { return nil }, codes.Internal},
		"denial ignored":   {func(ctx context.Context) error { _ = authz.Check(ctx, opPath); return nil }, codes.Internal},
		"state leak first": {func(context.Context) error { return apierr.InvalidState(opID, "DONE", "cancel") }, codes.Internal},
		"not found first":  {func(context.Context) error { return apierr.NotFound("operation", opID) }, codes.NotFound},
		"invalid first": {func(context.Context) error {
			return apierr.InvalidFields(apierr.FieldViolation{Field: "operation_id", Description: "bad"})
		}, codes.InvalidArgument},
		"denial returned": {func(ctx context.Context) error { return authz.Check(ctx, opPath) }, codes.PermissionDenied},
	} {
		e.ops.cancel = tc.handler
		if err := call(); code(err) != tc.want {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}
	}

	// A later allowing check does not wash out an earlier denial.
	e.access.Grant(alice, canPerm, authz.Resource{Type: "project", ID: project})
	e.ops.cancel = func(ctx context.Context) error {
		_ = authz.Check(ctx, authz.ProjectPath(org, other)) // denied, ignored
		return authz.Check(ctx, opPath)                     // allowed
	}
	if err := call(); code(err) != codes.Internal {
		t.Errorf("denial then allow: %v, want INTERNAL", err)
	}
}

// Require checks dependent resources without replacing the method's check.
func TestRequire(t *testing.T) {
	e := newEnv(t)
	e.access.Grant(alice, canPerm, authz.Resource{Type: "project", ID: project})
	subnet := authz.ProjectPath(org, project).Child("subnet", "sub-1")
	e.ops.cancel = func(ctx context.Context) error {
		if err := authz.Check(ctx, opPath); err != nil {
			return err
		}
		return authz.Require(ctx, "vpc.subnets.use", subnet)
	}
	_, err := e.client.CancelOperation(as(alice), &operationv1.CancelOperationRequest{OperationId: opID})
	if code(err) != codes.PermissionDenied || !apierr.IsReason(err, apipb.ErrorReason_QS_PERMISSION_DENIED) {
		t.Errorf("without vpc.subnets.use: %v", err)
	}
	e.access.Grant(alice, "vpc.subnets.use", authz.Resource{Type: "subnet", ID: "sub-1"})
	if _, err := e.client.CancelOperation(as(alice), &operationv1.CancelOperationRequest{OperationId: opID}); err != nil {
		t.Errorf("with vpc.subnets.use: %v", err)
	}
	e.ops.cancel = func(ctx context.Context) error { return authz.Require(ctx, "vpc.subnets.use", subnet) }
	if _, err := e.client.CancelOperation(as(alice), &operationv1.CancelOperationRequest{OperationId: opID}); code(err) != codes.Internal {
		t.Errorf("Require alone must not count as the method's check: %v", err)
	}
}

// Fail closed when IAM is unavailable: UNAVAILABLE, not PERMISSION_DENIED.
func TestAccessServiceFailure(t *testing.T) {
	e := newEnv(t)
	e.access.FailWith(errors.New("connection refused"))
	_, err := e.client.GetOperation(as(alice), &operationv1.GetOperationRequest{OperationId: opID})
	if code(err) != codes.Unavailable {
		t.Errorf("err = %v, want UNAVAILABLE", err)
	}
}

// Roadmap T-0.13 acceptance: a method without a permission annotation is
// reported by CheckServer (so the service's test fails) and rejected at
// runtime; listed public methods pass without a subject.
func TestUnannotatedMethods(t *testing.T) {
	e := newEnv(t)
	problems := authz.CheckServer(e.srv, public)
	if len(problems) == 0 || !slices.Contains(problems, "grpc.health.v1.Health.Watch: missing (qstack.api.permission)") {
		t.Errorf("CheckServer = %q, want Health.Watch reported", problems)
	}
	for _, p := range problems {
		if !strings.HasPrefix(p, "grpc.health.v1.Health.") {
			t.Errorf("annotated OperationService reported: %s", p)
		}
	}
	allHealth := map[string]bool{}
	for _, m := range e.srv.GetServiceInfo()["grpc.health.v1.Health"].Methods {
		allHealth["/grpc.health.v1.Health/"+m.Name] = true
	}
	if got := authz.CheckServer(e.srv, allHealth); len(got) != 0 {
		t.Errorf("with all health methods public: %q", got)
	}

	if _, err := e.health.Check(context.Background(), &healthpb.HealthCheckRequest{}); err != nil {
		t.Errorf("public method: %v", err)
	}
	stream, err := e.health.Watch(as(alice), &healthpb.HealthCheckRequest{})
	if err == nil {
		_, err = stream.Recv()
	}
	if code(err) != codes.Internal {
		t.Errorf("unannotated stream method: %v, want INTERNAL", err)
	}
}

// REQ-IAM-011: decisions, allow and deny, are cached for 5 s; errors are not.
func TestClientCache(t *testing.T) {
	access := authzfake.New()
	srv := grpc.NewServer()
	iaminternalv1.RegisterAccessServiceServer(srv, access.Server())
	clk := clock.NewFake(time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC))
	c := authz.NewClient(serve(t, srv), authz.ClientOptions{Clock: clk, CacheSize: 2})
	ctx := context.Background()
	path := authz.ProjectPath(org, project)

	if ok, err := c.Authorize(ctx, alice, getPerm, path); ok || err != nil {
		t.Fatalf("first = %v, %v", ok, err)
	}
	access.Grant(alice, getPerm, authz.Resource{Type: "project", ID: project})
	if ok, _ := c.Authorize(ctx, alice, getPerm, path); ok || access.Calls() != 1 {
		t.Errorf("deny not cached: %v after %d calls", ok, access.Calls())
	}
	clk.Advance(authz.DefaultCacheTTL)
	if ok, _ := c.Authorize(ctx, alice, getPerm, path); !ok || access.Calls() != 2 {
		t.Errorf("after TTL: %v after %d calls", ok, access.Calls())
	}
	access.Revoke(alice, getPerm, authz.Resource{Type: "project", ID: project})
	if ok, _ := c.Authorize(ctx, alice, getPerm, path); !ok || access.Calls() != 2 {
		t.Error("allow not cached")
	}

	access.FailWith(errors.New("down"))
	if _, err := c.Authorize(ctx, alice, canPerm, path); err == nil {
		t.Fatal("error not returned")
	}
	access.FailWith(nil)
	calls := access.Calls()
	if _, err := c.Authorize(ctx, alice, canPerm, path); err != nil || access.Calls() != calls+1 {
		t.Error("error was cached")
	}

	// LRU bound: a third key evicts the least recently used.
	_, _ = c.Authorize(ctx, alice, listPerm, path)
	calls = access.Calls()
	_, _ = c.Authorize(ctx, alice, getPerm, path) // evicted
	if access.Calls() != calls+1 {
		t.Error("cache exceeded its size")
	}
}
