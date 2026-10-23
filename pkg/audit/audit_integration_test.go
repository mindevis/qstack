//go:build integration

package audit_test

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/genproto/googleapis/rpc/code"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/wrapperspb"

	apipb "github.com/mindevis/qstack/gen/go/qstack/api"
	auditv1 "github.com/mindevis/qstack/gen/go/qstack/audit/v1"
	operationv1 "github.com/mindevis/qstack/gen/go/qstack/operation/v1"
	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/audit"
	"github.com/mindevis/qstack/pkg/authz"
	"github.com/mindevis/qstack/pkg/authz/authzfake"
	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/db"
	"github.com/mindevis/qstack/pkg/events"
	"github.com/mindevis/qstack/pkg/ids"
	"github.com/mindevis/qstack/pkg/operation"
	"github.com/mindevis/qstack/pkg/outbox"
	"github.com/mindevis/qstack/pkg/testenv"
)

const (
	org     = "org-01m5hqpzg00000000000000000"
	project = "prj-01m5hqpzg00000000000000000"
	alice   = "usr-01m5hqpzg00000000000000000"
	getPerm = "operations.operations.get"
	canPerm = "operations.operations.cancel"
)

// service: CancelOperation is a mutating method that, like real Create
// methods, authorizes and creates an Operation in a transaction. after
// injects behaviour after the operation is created.
type service struct {
	operationv1.UnimplementedOperationServiceServer
	pool  *pgxpool.Pool
	ops   *operation.Store
	ids   *ids.Generator
	after func(commit func() error) error
}

func (s *service) target(id string) authz.Path {
	return authz.ProjectPath(org, project).Child("operation", id)
}

func (s *service) GetOperation(ctx context.Context, req *operationv1.GetOperationRequest) (*operationv1.Operation, error) {
	if err := authz.CheckResource(ctx, s.target(req.GetOperationId()), getPerm); err != nil {
		return nil, err
	}
	return &operationv1.Operation{Id: req.GetOperationId()}, nil
}

func (s *service) CancelOperation(ctx context.Context, req *operationv1.CancelOperationRequest) (*operationv1.Operation, error) {
	if err := authz.CheckResource(ctx, s.target(req.GetOperationId()), getPerm); err != nil {
		return nil, err
	}
	resource := s.ids.New(ids.Instance)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	op, err := s.ops.Create(ctx, tx, operation.Spec{ProjectID: project, ResourceID: resource,
		Description: "Cancel", CreatedBy: authz.SubjectID(ctx), Metadata: wrapperspb.String(resource)})
	if err != nil {
		return nil, err
	}
	commit := func() error { return tx.Commit(ctx) }
	if s.after != nil {
		if err := s.after(commit); err != nil {
			return nil, err
		}
		return op, nil
	}
	return op, commit()
}

type env struct {
	pool   *pgxpool.Pool
	svc    *service
	access *authzfake.AccessService
	client operationv1.OperationServiceClient
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, db.Config{DSN: testenv.Postgres(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, m := range []func(context.Context, *pgxpool.Pool) ([]string, error){outbox.Migrate, operation.Migrate} {
		if _, err := m(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	clk := clock.Real()
	gen := ids.NewGenerator(clk, rand.Reader)
	w, err := outbox.NewWriter(events.Source{Region: "ru-central1", Service: "operations"}, clk, gen)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.DiscardHandler)
	auditor, err := audit.New(audit.Options{Service: "operations", Region: "ru-central1", Pool: pool, Outbox: w, Clock: clk, IDs: gen, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{pool: pool, access: authzfake.New()}
	e.svc = &service{pool: pool, ids: gen, ops: operation.New(pool, clk, gen, operation.WithCreateHook(auditor.OperationCreated))}

	authOpts := authz.Options{Authorizer: e.access, Logger: logger}
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(
		func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			md, _ := metadata.FromIncomingContext(ctx)
			if v := md.Get("x-test-subject"); len(v) == 1 {
				ctx = authz.WithSubject(ctx, authz.Subject{ID: v[0], Type: "user", AuthMethod: "token"})
			}
			return h(ctx, req)
		},
		auditor.UnaryServerInterceptor(),
		authz.UnaryServerInterceptor(authOpts)))
	operationv1.RegisterOperationServiceServer(srv, e.svc)
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
	e.client = operationv1.NewOperationServiceClient(conn)
	return e
}

// audited returns the audit events in the outbox.
func (e *env) audited(t *testing.T) []*auditv1.AuditEvent {
	t.Helper()
	rows, err := e.pool.Query(context.Background(), `SELECT subject, payload FROM outbox ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		subject string
		payload []byte
	}
	rs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		return x, r.Scan(&x.subject, &x.payload)
	})
	if err != nil {
		t.Fatal(err)
	}
	var out []*auditv1.AuditEvent
	for _, r := range rs {
		if r.subject != "qs.ru-central1.audit.operations" {
			t.Errorf("subject %q", r.subject)
		}
		ev, err := events.Unmarshal(r.payload)
		if err != nil || ev.Type != audit.EventType {
			t.Fatalf("envelope %+v: %v", ev, err)
		}
		a := &auditv1.AuditEvent{}
		if err := ev.DecodeData(a); err != nil {
			t.Fatal(err)
		}
		if a.GetId() != ev.ID {
			t.Errorf("audit id %s != event id %s", a.GetId(), ev.ID)
		}
		out = append(out, a)
	}
	return out
}

func (e *env) cancel(subject, id string) (*operationv1.Operation, error) {
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-test-subject", subject, "x-forwarded-for", "203.0.113.7, 10.0.0.1")
	return e.client.CancelOperation(ctx, &operationv1.CancelOperationRequest{OperationId: id})
}

const target = "op-01m5hqpzg00000000000000000"

// REQ-OBS-010: a successful mutating call is audited in the transaction of
// its effect, with caller, targets, request and operation.
func TestSuccessIsAuditedWithTheChange(t *testing.T) {
	e := newEnv(t)
	e.access.Grant(alice, canPerm, authz.Resource{Type: "project", ID: project})
	op, err := e.cancel(alice, target)
	if err != nil {
		t.Fatal(err)
	}
	evs := e.audited(t)
	if len(evs) != 1 {
		t.Fatalf("%d audit events, want 1", len(evs))
	}
	a := evs[0]
	if a.GetStatusCode() != code.Code_OK || a.GetOperationId() != op.GetId() || a.GetSubjectId() != alice ||
		a.GetSubjectType() != "user" || a.GetAuthMethod() != "token" || a.GetSourceIp() != "203.0.113.7" ||
		a.GetService() != "operations" || a.GetMethod() != "/qstack.operation.v1.OperationService/CancelOperation" ||
		a.GetProjectId() != project || a.GetOrganizationId() != org || a.GetUserAgent() == "" {
		t.Errorf("event %v", a)
	}
	if len(a.GetResourceIds()) != 2 || a.GetResourceIds()[0] != target || a.GetResourceIds()[1] != op.GetResourceId() {
		t.Errorf("resource ids %v, want [%s %s]", a.GetResourceIds(), target, op.GetResourceId())
	}
	if a.GetRequest().GetFields()["operationId"].GetStringValue() != target {
		t.Errorf("request %v", a.GetRequest())
	}
}

// REQ-OBS-010: a call denied by IAM is audited too, with what it targeted.
func TestDenialIsAudited(t *testing.T) {
	e := newEnv(t)
	e.access.Grant(alice, getPerm, authz.Resource{Type: "project", ID: project})
	if _, err := e.cancel(alice, target); !apierr.IsReason(err, apipb.ErrorReason_QS_PERMISSION_DENIED) {
		t.Fatalf("err = %v", err)
	}
	evs := e.audited(t)
	if len(evs) != 1 || evs[0].GetStatusCode() != code.Code_PERMISSION_DENIED ||
		evs[0].GetStatusReason() != "QS_PERMISSION_DENIED" || evs[0].GetProjectId() != project ||
		len(evs[0].GetResourceIds()) != 1 || evs[0].GetResourceIds()[0] != target {
		t.Errorf("events %v", evs)
	}
	if _, err := e.cancel("", target); err == nil {
		t.Fatal("unauthenticated call succeeded")
	}
	if evs := e.audited(t); len(evs) != 2 || evs[1].GetStatusCode() != code.Code_UNAUTHENTICATED || evs[1].GetSubjectId() != "" {
		t.Errorf("unauthenticated call not audited: %v", evs)
	}
}

// A handler that rolls back after creating the operation leaves an event
// with its error; one that commits and then fails leaves exactly the
// committed event.
func TestFailureAfterCreate(t *testing.T) {
	e := newEnv(t)
	e.access.Grant(alice, canPerm, authz.Resource{Type: "project", ID: project})

	e.svc.after = func(func() error) error { return apierr.ZoneCapacity("ru-central1-a") } // rolled back
	if _, err := e.cancel(alice, target); err == nil {
		t.Fatal("want error")
	}
	evs := e.audited(t)
	if len(evs) != 1 || evs[0].GetStatusCode() != code.Code_RESOURCE_EXHAUSTED || evs[0].GetStatusReason() != "QS_ZONE_CAPACITY" {
		t.Fatalf("after rollback: %v", evs)
	}

	e.svc.after = func(commit func() error) error {
		if err := commit(); err != nil {
			return err
		}
		return errors.New("failure after commit")
	}
	if _, err := e.cancel(alice, target); err == nil {
		t.Fatal("want error")
	}
	evs = e.audited(t)
	if len(evs) != 2 || evs[1].GetStatusCode() != code.Code_OK || evs[1].GetOperationId() == "" {
		t.Errorf("after commit then failure: %v (want the committed event only)", evs)
	}
}

// Reads are not audited.
func TestReadsAreNotAudited(t *testing.T) {
	e := newEnv(t)
	e.access.Grant(alice, getPerm, authz.Resource{Type: "project", ID: project})
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-test-subject", alice)
	if _, err := e.client.GetOperation(ctx, &operationv1.GetOperationRequest{OperationId: target}); err != nil {
		t.Fatal(err)
	}
	if evs := e.audited(t); len(evs) != 0 {
		t.Errorf("read audited: %v", evs)
	}
}
