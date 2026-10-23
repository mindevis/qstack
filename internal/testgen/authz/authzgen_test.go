package authzgen_test

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	operationv1 "github.com/mindevis/qstack/gen/go/qstack/operation/v1"
	authzgen "github.com/mindevis/qstack/internal/testgen/authz"
	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/authz"
	"github.com/mindevis/qstack/pkg/authz/authzfake"
	"github.com/mindevis/qstack/pkg/ids"
)

const (
	org         = "org-01m5hqpzg00000000000000000"
	project     = "prj-01m5hqpzg00000000000000000"
	other       = "prj-01m5hqpzg00000000000000001"
	getPerm     = "operations.operations.get"
	projectsGet = "resourcemanager.projects.get"
)

// opService is the "test service": OperationService over an in-memory store,
// authorizing as spec 03 §4 prescribes. Flags inject typical bugs.
type opService struct {
	operationv1.UnimplementedOperationServiceServer
	mu  sync.Mutex
	ops map[string]string // operation id → project id

	listWithoutProjectGet bool // List uses Check: reveals foreign projects
	cancelNeedsGet        bool // Cancel also Requires get
}

func (s *opService) add(projectID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := ids.New(ids.Operation)
	s.ops[id] = projectID
	return id
}

func (s *opService) load(id string) (authz.Path, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.ops[id]
	if !ok {
		return nil, apierr.NotFound("operation", id)
	}
	return authz.ProjectPath(org, p).Child("operation", id), nil
}

func (s *opService) GetOperation(ctx context.Context, req *operationv1.GetOperationRequest) (*operationv1.Operation, error) {
	path, err := s.load(req.GetOperationId())
	if err != nil {
		return nil, err
	}
	if err := authz.CheckResource(ctx, path, getPerm); err != nil {
		return nil, err
	}
	return &operationv1.Operation{Id: req.GetOperationId()}, nil
}

func (s *opService) ListOperations(ctx context.Context, req *operationv1.ListOperationsRequest) (*operationv1.ListOperationsResponse, error) {
	path := authz.ProjectPath(org, req.GetProjectId())
	check := func() error { return authz.CheckResource(ctx, path, projectsGet) }
	if s.listWithoutProjectGet {
		check = func() error { return authz.Check(ctx, path) }
	}
	if err := check(); err != nil {
		return nil, err
	}
	return &operationv1.ListOperationsResponse{}, nil
}

func (s *opService) CancelOperation(ctx context.Context, req *operationv1.CancelOperationRequest) (*operationv1.Operation, error) {
	path, err := s.load(req.GetOperationId())
	if err != nil {
		return nil, err
	}
	if err := authz.CheckResource(ctx, path, getPerm); err != nil {
		return nil, err
	}
	if s.cancelNeedsGet {
		if err := authz.Require(ctx, getPerm, path); err != nil {
			return nil, err
		}
	}
	return &operationv1.Operation{Id: req.GetOperationId(), Done: true}, nil
}

func subjectFromMetadata(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("x-test-subject"); len(v) == 1 {
		ctx = authz.WithSubject(ctx, authz.Subject{ID: v[0], Type: "user"})
	}
	return h(ctx, req)
}

var public = map[string]bool{
	"/grpc.health.v1.Health/Check": true,
	"/grpc.health.v1.Health/List":  true,
	"/grpc.health.v1.Health/Watch": true,
}

// suite starts the service and returns the generator's input.
func suite(t *testing.T, svc *opService, pub map[string]bool) authzgen.Suite {
	t.Helper()
	svc.ops = map[string]string{}
	access := authzfake.New()
	opts := authz.Options{Authorizer: access, Public: pub, Logger: slog.New(slog.DiscardHandler)}
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(subjectFromMetadata, authz.UnaryServerInterceptor(opts)),
		grpc.StreamInterceptor(authz.StreamServerInterceptor(opts)))
	operationv1.RegisterOperationServiceServer(srv, svc)
	healthpb.RegisterHealthServer(srv, health.NewServer())

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

	return authzgen.Suite{
		Conn: conn, Server: srv, Public: pub, Access: access,
		As: func(ctx context.Context, subject string) context.Context {
			return metadata.AppendToOutgoingContext(ctx, "x-test-subject", subject)
		},
		Project:      authz.Resource{Type: "project", ID: project},
		OtherProject: authz.Resource{Type: "project", ID: other},
		Requests: map[string]authzgen.Request{
			"/qstack.operation.v1.OperationService/GetOperation": {Build: func(context.Context) (proto.Message, error) {
				return &operationv1.GetOperationRequest{OperationId: svc.add(project)}, nil
			}},
			"/qstack.operation.v1.OperationService/ListOperations": {Build: func(context.Context) (proto.Message, error) {
				return &operationv1.ListOperationsRequest{Scope: &operationv1.ListOperationsRequest_ProjectId{ProjectId: project}}, nil
			}},
			"/qstack.operation.v1.OperationService/CancelOperation": {Build: func(context.Context) (proto.Message, error) {
				return &operationv1.CancelOperationRequest{OperationId: svc.add(project)}, nil
			}},
		},
	}
}

// Roadmap T-0.16 acceptance: tests are generated for the test service and pass.
func TestCorrectServicePasses(t *testing.T) {
	s := suite(t, &opService{}, public)
	cases, err := authzgen.Generate(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 3*4 {
		t.Fatalf("%d cases, want 12 (3 methods × 4)", len(cases))
	}
	authzgen.Run(t, s)
}

// failures runs all cases and returns "method/case" of the failing ones.
func failures(t *testing.T, s authzgen.Suite) []string {
	t.Helper()
	cases, err := authzgen.Generate(s)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range cases {
		if c.Run(context.Background()) != nil {
			out = append(out, c.Method[strings.LastIndex(c.Method, "/")+1:]+"/"+c.Name)
		}
	}
	return out
}

// REQ-SEC-004: a List that reveals another tenant's project is caught.
func TestCatchesForeignProjectLeak(t *testing.T) {
	got := failures(t, suite(t, &opService{listWithoutProjectGet: true}, public))
	if len(got) != 1 || got[0] != "ListOperations/other-project" {
		t.Errorf("failures = %v, want [ListOperations/other-project]", got)
	}
}

// A handler checking an undeclared permission fails the allowed case until
// the permission is declared in Extra.
func TestCatchesUndeclaredPermission(t *testing.T) {
	svc := &opService{cancelNeedsGet: true}
	s := suite(t, svc, public)
	if got := failures(t, s); len(got) != 1 || got[0] != "CancelOperation/allowed" {
		t.Errorf("failures = %v, want [CancelOperation/allowed]", got)
	}
	r := s.Requests["/qstack.operation.v1.OperationService/CancelOperation"]
	r.Extra = []string{getPerm}
	s.Requests["/qstack.operation.v1.OperationService/CancelOperation"] = r
	if got := failures(t, s); len(got) != 0 {
		t.Errorf("with Extra declared: failures = %v", got)
	}
}

// Every method needs a request builder and a permission (100% coverage).
func TestRequiresEveryMethod(t *testing.T) {
	s := suite(t, &opService{}, public)
	delete(s.Requests, "/qstack.operation.v1.OperationService/CancelOperation")
	if _, err := authzgen.Generate(s); err == nil || !strings.Contains(err.Error(), "CancelOperation") {
		t.Errorf("missing builder: %v", err)
	}
	s = suite(t, &opService{}, map[string]bool{"/grpc.health.v1.Health/Check": true})
	if _, err := authzgen.Generate(s); err == nil || !strings.Contains(err.Error(), "Health.Watch") {
		t.Errorf("unannotated method: %v", err)
	}
}
