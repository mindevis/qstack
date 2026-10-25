//go:build integration

package api_test

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	accessv1 "github.com/mindevis/qstack/gen/go/qstack/access/v1"
	operationv1 "github.com/mindevis/qstack/gen/go/qstack/operation/v1"
	rminternalv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/internalv1"
	rmv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/v1"
	"github.com/mindevis/qstack/internal/iam/catalog"
	"github.com/mindevis/qstack/internal/resourcemanager/api"
	"github.com/mindevis/qstack/internal/resourcemanager/app"
	"github.com/mindevis/qstack/internal/resourcemanager/domain"
	"github.com/mindevis/qstack/internal/resourcemanager/store"
	authzgen "github.com/mindevis/qstack/internal/testgen/authz"
	"github.com/mindevis/qstack/pkg/audit"
	"github.com/mindevis/qstack/pkg/authz"
	"github.com/mindevis/qstack/pkg/authz/authzfake"
	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/db"
	"github.com/mindevis/qstack/pkg/idempotency"
	"github.com/mindevis/qstack/pkg/ids"
	"github.com/mindevis/qstack/pkg/operation"
	"github.com/mindevis/qstack/pkg/outbox"
	"github.com/mindevis/qstack/pkg/pagination"
	"github.com/mindevis/qstack/pkg/testenv"
	"github.com/mindevis/qstack/pkg/validate"
)

// Every permission of the Resource Manager API.
var permissions = []string{
	"resourcemanager.organizations.get", "resourcemanager.organizations.list", "resourcemanager.organizations.update",
	"resourcemanager.organizations.listAccessBindings", "resourcemanager.organizations.setAccessBindings",
	"resourcemanager.organizations.updateAccessBindings",
	"resourcemanager.projects.get", "resourcemanager.projects.list", "resourcemanager.projects.create",
	"resourcemanager.projects.update", "resourcemanager.projects.delete",
	"resourcemanager.projects.listAccessBindings", "resourcemanager.projects.updateAccessBindings",
	"resourcemanager.projects.getQuotas",
}

type env struct {
	pool     *pgxpool.Pool
	app      *app.App
	access   *authzfake.AccessService
	srv      *grpc.Server
	conn     *grpc.ClientConn
	orgs     rmv1.OrganizationServiceClient
	projects rmv1.ProjectServiceClient
	internal rminternalv1.OrganizationServiceClient
	quotas   rminternalv1.QuotaServiceClient
}

// authenticate stands in for token verification: x-test-subject → Subject.
func authenticate(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("x-test-subject"); len(v) == 1 {
		ctx = authz.WithSubject(ctx, authz.Subject{ID: v[0], Type: "user", AuthMethod: "token"})
	}
	return h(ctx, req)
}

func as(ctx context.Context, subject string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "x-test-subject", subject)
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := t.Context()
	pool, err := db.Open(ctx, db.Config{DSN: testenv.Postgres(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, m := range []func(context.Context, *pgxpool.Pool) ([]string, error){store.Migrate, operation.Migrate, outbox.Migrate} {
		if _, err := m(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	logger := slog.New(slog.DiscardHandler)
	clk := clock.Real()
	gen := ids.NewGenerator(clk, rand.Reader)
	ow, err := outbox.NewWriter(app.Source, clk, gen)
	if err != nil {
		t.Fatal(err)
	}
	auditor, err := audit.New(audit.Options{Service: "resourcemanager", Region: "global", Pool: pool, Outbox: ow, Clock: clk, IDs: gen, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	ops := operation.New(pool, clk, gen, operation.WithCreateHook(auditor.OperationCreated))
	roles, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(app.Config{Pool: pool, Operations: ops, Outbox: ow, IDs: gen, Clock: clk, Roles: roles, QuotaDefaults: domain.DefaultQuotas})
	if err != nil {
		t.Fatal(err)
	}
	v, err := validate.New()
	if err != nil {
		t.Fatal(err)
	}
	pages, err := pagination.NewSigner(clk, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{pool: pool, app: a, access: authzfake.New()}
	e.srv, err = api.NewServer(api.Options{
		App: a, Authorizer: e.access, Authenticate: authenticate, Auditor: auditor, Operations: ops,
		Pages: pages, Validator: v, Logger: logger, Health: health.NewServer(),
	})
	if err != nil {
		t.Fatal(err)
	}
	e.conn = serve(t, e.srv)
	e.orgs = rmv1.NewOrganizationServiceClient(e.conn)
	e.projects = rmv1.NewProjectServiceClient(e.conn)
	internal := serve(t, api.NewInternalServer(a, v, logger))
	e.internal = rminternalv1.NewOrganizationServiceClient(internal)
	e.quotas = rminternalv1.NewQuotaServiceClient(internal)
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

// createOrg creates an active organization owned by owner, who gets every
// permission on it in the fake access service.
func (e *env) createOrg(t *testing.T, name, owner string) *rmv1.Organization {
	t.Helper()
	o, err := e.internal.CreateOrganization(t.Context(), &rminternalv1.CreateOrganizationRequest{
		Name: name, DisplayName: name, OwnerUserId: owner, Status: rmv1.Organization_STATUS_ACTIVE,
	})
	if err != nil {
		t.Fatalf("create organization %s: %v", name, err)
	}
	e.grantAll(owner, authz.Resource{Type: "organization", ID: o.GetId()})
	return o
}

func (e *env) grantAll(subject string, r authz.Resource) {
	for _, p := range permissions {
		e.access.Grant(subject, p, r)
	}
}

// done unpacks the response of a finished operation.
func done[T proto.Message](t *testing.T, op *operationv1.Operation, err error, resp T) T {
	t.Helper()
	if err != nil {
		t.Fatalf("operation: %v", err)
	}
	if !op.GetDone() || op.GetError() != nil {
		t.Fatalf("operation %s not done or failed: %v", op.GetId(), op.GetError())
	}
	if err := op.GetResponse().UnmarshalTo(resp); err != nil {
		t.Fatalf("operation response: %v", err)
	}
	return resp
}

func code(err error) codes.Code { return status.Code(err) }

func newUser() string { return ids.New(ids.User) }

// e2e acceptance of T-1.2: project CRUD through the gRPC API with the
// platform interceptor chain (REQ-PLT-030, 031, 032, 040, 041, 022).
func TestProjectCRUD(t *testing.T) {
	e := newEnv(t)
	alice := newUser()
	org := e.createOrg(t, "acme", alice)
	ctx := as(t.Context(), alice)

	create := func(name string, labels map[string]string) *rmv1.Project {
		op, err := e.projects.CreateProject(ctx, &rmv1.CreateProjectRequest{OrganizationId: org.GetId(), Name: name, Labels: labels})
		p := done(t, op, err, &rmv1.Project{})
		if meta := (&rmv1.CreateProjectMetadata{}); op.GetMetadata().UnmarshalTo(meta) != nil || meta.GetProjectId() != p.GetId() {
			t.Errorf("metadata %v does not name project %s (REQ-PLT-041)", op.GetMetadata(), p.GetId())
		}
		return p
	}
	web := create("web", map[string]string{"env": "prod"})
	create("api", map[string]string{"env": "prod"})
	create("batch", map[string]string{"env": "dev"})
	if web.GetStatus() != rmv1.Project_STATUS_ACTIVE || web.GetOrganizationId() != org.GetId() || web.GetEtag() == "" {
		t.Errorf("created project: %v", web)
	}

	got, err := e.projects.GetProject(ctx, &rmv1.GetProjectRequest{ProjectId: web.GetId()})
	if err != nil || !proto.Equal(got, web) {
		t.Fatalf("GetProject = %v, %v; want %v", got, err, web)
	}

	// Filter and keyset pages.
	list, err := e.projects.ListProjects(ctx, &rmv1.ListProjectsRequest{OrganizationId: org.GetId(), Filter: "labels.env = prod", OrderBy: "name"})
	if err != nil {
		t.Fatal(err)
	}
	if names := projectNames(list.GetProjects()); !slices.Equal(names, []string{"api", "web"}) {
		t.Errorf("filtered list = %v, want [api web]", names)
	}
	var all []string
	req := &rmv1.ListProjectsRequest{OrganizationId: org.GetId(), PageSize: 2, OrderBy: "name desc"}
	for {
		page, err := e.projects.ListProjects(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, projectNames(page.GetProjects())...)
		if page.GetNextPageToken() == "" {
			break
		}
		req.PageToken = page.GetNextPageToken()
	}
	if !slices.Equal(all, []string{"web", "batch", "api"}) {
		t.Errorf("paged list = %v, want [web batch api]", all)
	}
	req.OrderBy = "name"
	if _, err := e.projects.ListProjects(ctx, req); code(err) != codes.InvalidArgument {
		t.Errorf("page token with another order_by: %v, want INVALID_ARGUMENT", err)
	}
	if _, err := e.projects.ListProjects(ctx, &rmv1.ListProjectsRequest{OrganizationId: org.GetId(), Filter: "owner = x"}); code(err) != codes.InvalidArgument {
		t.Errorf("unknown filter field: %v, want INVALID_ARGUMENT", err)
	}

	// Update with etag; a stale etag is ABORTED.
	op, err := e.projects.UpdateProject(ctx, &rmv1.UpdateProjectRequest{
		ProjectId: web.GetId(), UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"name", "description"}},
		Name: "www", Description: "public site", Labels: map[string]string{"ignored": "x"}, Etag: web.GetEtag(),
	})
	updated := done(t, op, err, &rmv1.Project{})
	if updated.GetName() != "www" || updated.GetDescription() != "public site" || updated.GetLabels()["env"] != "prod" || updated.GetEtag() == web.GetEtag() {
		t.Errorf("updated project: %v", updated)
	}
	if _, err := e.projects.UpdateProject(ctx, &rmv1.UpdateProjectRequest{
		ProjectId: web.GetId(), UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"description"}}, Etag: web.GetEtag(),
	}); code(err) != codes.Aborted {
		t.Errorf("stale etag: %v, want ABORTED", err)
	}
	if _, err := e.projects.UpdateProject(ctx, &rmv1.UpdateProjectRequest{
		ProjectId: web.GetId(), UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"name"}}, Name: "api",
	}); code(err) != codes.AlreadyExists {
		t.Errorf("rename to a taken name: %v, want ALREADY_EXISTS", err)
	}
	if _, err := e.projects.UpdateProject(ctx, &rmv1.UpdateProjectRequest{ProjectId: web.GetId()}); code(err) != codes.InvalidArgument {
		t.Errorf("no update mask: %v, want INVALID_ARGUMENT", err)
	}

	// Delete; the name becomes free.
	op, err = e.projects.DeleteProject(ctx, &rmv1.DeleteProjectRequest{ProjectId: web.GetId(), Etag: updated.GetEtag()})
	if err != nil || !op.GetDone() || op.GetError() != nil {
		t.Fatalf("DeleteProject = %v, %v", op, err)
	}
	if _, err := e.projects.GetProject(ctx, &rmv1.GetProjectRequest{ProjectId: web.GetId()}); code(err) != codes.NotFound {
		t.Errorf("get deleted project: %v, want NOT_FOUND", err)
	}
	if _, err := e.projects.DeleteProject(ctx, &rmv1.DeleteProjectRequest{ProjectId: web.GetId()}); code(err) != codes.NotFound {
		t.Errorf("delete again: %v, want NOT_FOUND", err)
	}
	list, err = e.projects.ListProjects(ctx, &rmv1.ListProjectsRequest{OrganizationId: org.GetId()})
	if err != nil || len(list.GetProjects()) != 2 {
		t.Errorf("list after delete = %v, %v; want 2 projects", projectNames(list.GetProjects()), err)
	}
	if again := create("www", nil); again.GetId() == web.GetId() {
		t.Error("recreated project reuses the deleted ID")
	}

	if n := e.outboxCount(t, "qs.global.resourcemanager.project.created"); n != 4 {
		t.Errorf("project.created events = %d, want 4 (REQ-PLT-090)", n)
	}
	if n := e.outboxCount(t, "qs.global.resourcemanager.project.deleted"); n != 1 {
		t.Errorf("project.deleted events = %d, want 1", n)
	}
}

func projectNames(ps []*rmv1.Project) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.GetName()
	}
	return out
}

func (e *env) outboxCount(t *testing.T, subject string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM outbox WHERE subject = $1`, subject).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// REQ-PLT-050: a repeated call with the same key returns the first
// Operation and creates nothing new.
func TestCreateProjectIdempotent(t *testing.T) {
	e := newEnv(t)
	alice := newUser()
	org := e.createOrg(t, "acme", alice)
	ctx := metadata.AppendToOutgoingContext(as(t.Context(), alice), idempotency.MetadataKey, "k-1")
	req := &rmv1.CreateProjectRequest{OrganizationId: org.GetId(), Name: "web"}
	first, err := e.projects.CreateProject(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.projects.CreateProject(ctx, req)
	if err != nil || second.GetId() != first.GetId() {
		t.Errorf("repeat = %v, %v; want operation %s", second.GetId(), err, first.GetId())
	}
	if _, err := e.projects.CreateProject(ctx, &rmv1.CreateProjectRequest{OrganizationId: org.GetId(), Name: "api"}); code(err) != codes.InvalidArgument {
		t.Errorf("same key, other body: %v, want INVALID_ARGUMENT", err)
	}
	if _, err := e.projects.CreateProject(as(t.Context(), alice), req); code(err) != codes.AlreadyExists {
		t.Errorf("same name without key: %v, want ALREADY_EXISTS", err)
	}
}

// Sign-up creates organizations through the internal API; only active
// organizations get projects.
func TestCreateOrganization(t *testing.T) {
	e := newEnv(t)
	alice, bob := newUser(), newUser()
	org := e.createOrg(t, "acme", alice)
	if org.GetOwnerUserId() != alice || org.GetStatus() != rmv1.Organization_STATUS_ACTIVE {
		t.Errorf("organization: %v", org)
	}
	again, err := e.internal.CreateOrganization(t.Context(), &rminternalv1.CreateOrganizationRequest{
		Name: "acme", OwnerUserId: alice, Status: rmv1.Organization_STATUS_ACTIVE,
	})
	if err != nil || again.GetId() != org.GetId() {
		t.Errorf("repeat = %v, %v; want %s", again.GetId(), err, org.GetId())
	}
	if _, err := e.internal.CreateOrganization(t.Context(), &rminternalv1.CreateOrganizationRequest{
		Name: "acme", OwnerUserId: bob, Status: rmv1.Organization_STATUS_ACTIVE,
	}); code(err) != codes.AlreadyExists {
		t.Errorf("name of another owner: %v, want ALREADY_EXISTS", err)
	}
	if _, err := e.internal.CreateOrganization(t.Context(), &rminternalv1.CreateOrganizationRequest{
		Name: "beta", OwnerUserId: bob, Status: rmv1.Organization_STATUS_SUSPENDED,
	}); code(err) != codes.InvalidArgument {
		t.Errorf("suspended on create: %v, want INVALID_ARGUMENT", err)
	}

	pending, err := e.internal.CreateOrganization(t.Context(), &rminternalv1.CreateOrganizationRequest{
		Name: "pending", OwnerUserId: bob, Status: rmv1.Organization_STATUS_PENDING_APPROVAL,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.grantAll(bob, authz.Resource{Type: "organization", ID: pending.GetId()})
	if _, err := e.projects.CreateProject(as(t.Context(), bob), &rmv1.CreateProjectRequest{
		OrganizationId: pending.GetId(), Name: "web",
	}); code(err) != codes.FailedPrecondition {
		t.Errorf("project in pending organization: %v, want FAILED_PRECONDITION", err)
	}

	bindings, err := e.orgs.ListAccessBindings(as(t.Context(), alice), &accessv1.ListAccessBindingsRequest{ResourceId: org.GetId()})
	if err != nil || len(bindings.GetAccessBindings()) != 1 ||
		bindings.GetAccessBindings()[0].GetRoleId() != "resource-manager.organizations.owner" ||
		bindings.GetAccessBindings()[0].GetSubject().GetId() != alice {
		t.Errorf("owner binding = %v, %v", bindings.GetAccessBindings(), err)
	}
}

func viewer(subject string) *accessv1.AccessBinding {
	return &accessv1.AccessBinding{RoleId: "viewer", Subject: &accessv1.Subject{Id: subject, Type: accessv1.Subject_TYPE_USER_ACCOUNT}}
}

func TestAccessBindings(t *testing.T) {
	e := newEnv(t)
	alice, bob := newUser(), newUser()
	org := e.createOrg(t, "acme", alice)
	ctx := as(t.Context(), alice)
	owner := &accessv1.AccessBinding{RoleId: "resource-manager.organizations.owner", Subject: &accessv1.Subject{Id: alice, Type: accessv1.Subject_TYPE_USER_ACCOUNT}}

	_, err := e.orgs.UpdateAccessBindings(ctx, &accessv1.UpdateAccessBindingsRequest{ResourceId: org.GetId(),
		AccessBindingDeltas: []*accessv1.AccessBindingDelta{{Action: accessv1.AccessBindingDelta_ACTION_ADD, AccessBinding: viewer(bob)}}})
	if err != nil {
		t.Fatal(err)
	}
	list, err := e.orgs.ListAccessBindings(ctx, &accessv1.ListAccessBindingsRequest{ResourceId: org.GetId(), PageSize: 1})
	if err != nil || len(list.GetAccessBindings()) != 1 || list.GetNextPageToken() == "" {
		t.Fatalf("first page = %v, %v", list, err)
	}
	next, err := e.orgs.ListAccessBindings(ctx, &accessv1.ListAccessBindingsRequest{ResourceId: org.GetId(), PageSize: 1, PageToken: list.GetNextPageToken()})
	if err != nil || len(next.GetAccessBindings()) != 1 || next.GetNextPageToken() != "" {
		t.Fatalf("second page = %v, %v", next, err)
	}

	unknown := &accessv1.AccessBinding{RoleId: "compute.nothing", Subject: &accessv1.Subject{Id: bob, Type: accessv1.Subject_TYPE_USER_ACCOUNT}}
	if _, err := e.orgs.UpdateAccessBindings(ctx, &accessv1.UpdateAccessBindingsRequest{ResourceId: org.GetId(),
		AccessBindingDeltas: []*accessv1.AccessBindingDelta{{Action: accessv1.AccessBindingDelta_ACTION_ADD, AccessBinding: unknown}}}); code(err) != codes.InvalidArgument {
		t.Errorf("role outside the catalog: %v, want INVALID_ARGUMENT", err)
	}

	// Spec 03 §2: the last owner cannot be removed.
	if _, err := e.orgs.UpdateAccessBindings(ctx, &accessv1.UpdateAccessBindingsRequest{ResourceId: org.GetId(),
		AccessBindingDeltas: []*accessv1.AccessBindingDelta{{Action: accessv1.AccessBindingDelta_ACTION_REMOVE, AccessBinding: owner}}}); code(err) != codes.FailedPrecondition {
		t.Errorf("remove last owner: %v, want FAILED_PRECONDITION", err)
	}

	// SetAccessBindings is guarded by the organization's etag, which
	// binding changes bump.
	if _, err := e.orgs.SetAccessBindings(ctx, &accessv1.SetAccessBindingsRequest{ResourceId: org.GetId(),
		AccessBindings: []*accessv1.AccessBinding{owner}, Etag: org.GetEtag()}); code(err) != codes.Aborted {
		t.Errorf("set with stale etag: %v, want ABORTED", err)
	}
	cur, err := e.orgs.GetOrganization(ctx, &rmv1.GetOrganizationRequest{OrganizationId: org.GetId()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.orgs.SetAccessBindings(ctx, &accessv1.SetAccessBindingsRequest{ResourceId: org.GetId(),
		AccessBindings: []*accessv1.AccessBinding{owner}, Etag: cur.GetEtag()}); err != nil {
		t.Fatalf("set: %v", err)
	}
	list, err = e.orgs.ListAccessBindings(ctx, &accessv1.ListAccessBindingsRequest{ResourceId: org.GetId()})
	if err != nil || len(list.GetAccessBindings()) != 1 || list.GetAccessBindings()[0].GetSubject().GetId() != alice {
		t.Errorf("after set = %v, %v; want only the owner", list.GetAccessBindings(), err)
	}

	// Projects hold their own bindings; the owner role is org-only.
	op, err := e.projects.CreateProject(ctx, &rmv1.CreateProjectRequest{OrganizationId: org.GetId(), Name: "web"})
	prj := done(t, op, err, &rmv1.Project{})
	if _, err := e.projects.UpdateAccessBindings(ctx, &accessv1.UpdateAccessBindingsRequest{ResourceId: prj.GetId(),
		AccessBindingDeltas: []*accessv1.AccessBindingDelta{{Action: accessv1.AccessBindingDelta_ACTION_ADD, AccessBinding: owner}}}); code(err) != codes.InvalidArgument {
		t.Errorf("owner role on a project: %v, want INVALID_ARGUMENT", err)
	}
	if _, err := e.projects.UpdateAccessBindings(ctx, &accessv1.UpdateAccessBindingsRequest{ResourceId: prj.GetId(),
		AccessBindingDeltas: []*accessv1.AccessBindingDelta{{Action: accessv1.AccessBindingDelta_ACTION_ADD, AccessBinding: viewer(bob)}}}); err != nil {
		t.Fatalf("project binding: %v", err)
	}
	if _, err := e.projects.ListAccessBindings(ctx, &accessv1.ListAccessBindingsRequest{ResourceId: org.GetId()}); code(err) != codes.InvalidArgument {
		t.Errorf("project service with an organization id: %v, want INVALID_ARGUMENT", err)
	}

	// Organization: 1 (create) + 2 (update, set); project: 1.
	if n := e.outboxCount(t, "qs.global.resourcemanager.accessbindings.updated"); n != 4 {
		t.Errorf("accessbindings.updated events = %d, want 4", n)
	}
}

// Spec 03 §6: ListOrganizations returns the organizations in which the
// caller holds a binding and may list; denials do not fail the call.
func TestListOrganizations(t *testing.T) {
	e := newEnv(t)
	alice, bob := newUser(), newUser()
	a := e.createOrg(t, "alpha", alice)
	b := e.createOrg(t, "beta", bob)
	e.createOrg(t, "gamma", bob)

	// alice gets a project binding in beta, none in gamma.
	op, err := e.projects.CreateProject(as(t.Context(), bob), &rmv1.CreateProjectRequest{OrganizationId: b.GetId(), Name: "web"})
	prj := done(t, op, err, &rmv1.Project{})
	if _, err := e.projects.UpdateAccessBindings(as(t.Context(), bob), &accessv1.UpdateAccessBindingsRequest{ResourceId: prj.GetId(),
		AccessBindingDeltas: []*accessv1.AccessBindingDelta{{Action: accessv1.AccessBindingDelta_ACTION_ADD, AccessBinding: viewer(alice)}}}); err != nil {
		t.Fatal(err)
	}

	list := func() []string {
		t.Helper()
		resp, err := e.orgs.ListOrganizations(as(t.Context(), alice), &rmv1.ListOrganizationsRequest{})
		if err != nil {
			t.Fatalf("ListOrganizations: %v", err)
		}
		var names []string
		for _, o := range resp.GetOrganizations() {
			names = append(names, o.GetName())
		}
		return names
	}
	if got := list(); !slices.Equal(got, []string{"alpha"}) {
		t.Errorf("list = %v, want [alpha]", got)
	}
	e.access.Grant(alice, "resourcemanager.organizations.list", authz.Resource{Type: "organization", ID: b.GetId()})
	if got := list(); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Errorf("list = %v, want [alpha beta]", got)
	}
	e.access.Revoke(alice, "resourcemanager.organizations.list", authz.Resource{Type: "organization", ID: a.GetId()})
	if got := list(); !slices.Equal(got, []string{"beta"}) {
		t.Errorf("list = %v, want [beta]", got)
	}
	if _, err := e.orgs.ListOrganizations(t.Context(), &rmv1.ListOrganizationsRequest{}); code(err) != codes.Unauthenticated {
		t.Errorf("unauthenticated: %v", err)
	}
}

// REQ-SEC-004, REQ-IAM-015: generated authorization cases for every method.
func TestAuthorization(t *testing.T) {
	e := newEnv(t)
	root := newUser()
	org := e.createOrg(t, "acme", root)
	other := e.createOrg(t, "other", newUser())
	rootCtx := as(t.Context(), root)

	op, err := e.projects.CreateProject(rootCtx, &rmv1.CreateProjectRequest{OrganizationId: org.GetId(), Name: "web"})
	prj := done(t, op, err, &rmv1.Project{})
	mask := func(p ...string) *fieldmaskpb.FieldMask { return &fieldmaskpb.FieldMask{Paths: p} }
	addViewer := []*accessv1.AccessBindingDelta{{Action: accessv1.AccessBindingDelta_ACTION_ADD, AccessBinding: viewer(newUser())}}
	static := func(m proto.Message) authzgen.Request {
		return authzgen.Request{Build: func(context.Context) (proto.Message, error) { return m, nil }}
	}

	authzgen.Run(t, authzgen.Suite{
		Conn: e.conn, Server: e.srv, Public: api.Public, Access: e.access, As: as,
		Project:      authz.Resource{Type: "organization", ID: org.GetId()},
		OtherProject: authz.Resource{Type: "organization", ID: other.GetId()},
		Skip: map[string]string{
			rmv1.OrganizationService_ListOrganizations_FullMethodName: "filters instead of refusing: TestListOrganizations",
		},
		Requests: map[string]authzgen.Request{
			rmv1.OrganizationService_GetOrganization_FullMethodName:      static(&rmv1.GetOrganizationRequest{OrganizationId: org.GetId()}),
			rmv1.OrganizationService_UpdateOrganization_FullMethodName:   static(&rmv1.UpdateOrganizationRequest{OrganizationId: org.GetId(), UpdateMask: mask("description"), Description: "x"}),
			rmv1.OrganizationService_ListAccessBindings_FullMethodName:   static(&accessv1.ListAccessBindingsRequest{ResourceId: org.GetId()}),
			rmv1.OrganizationService_UpdateAccessBindings_FullMethodName: static(&accessv1.UpdateAccessBindingsRequest{ResourceId: org.GetId(), AccessBindingDeltas: addViewer}),
			rmv1.OrganizationService_SetAccessBindings_FullMethodName: {Build: func(context.Context) (proto.Message, error) {
				cur, err := e.orgs.GetOrganization(rootCtx, &rmv1.GetOrganizationRequest{OrganizationId: org.GetId()})
				if err != nil {
					return nil, err
				}
				owner := &accessv1.AccessBinding{RoleId: "resource-manager.organizations.owner", Subject: &accessv1.Subject{Id: root, Type: accessv1.Subject_TYPE_USER_ACCOUNT}}
				return &accessv1.SetAccessBindingsRequest{ResourceId: org.GetId(), AccessBindings: []*accessv1.AccessBinding{owner}, Etag: cur.GetEtag()}, nil
			}},
			rmv1.ProjectService_GetProject_FullMethodName:   static(&rmv1.GetProjectRequest{ProjectId: prj.GetId()}),
			rmv1.ProjectService_ListProjects_FullMethodName: static(&rmv1.ListProjectsRequest{OrganizationId: org.GetId()}),
			rmv1.ProjectService_CreateProject_FullMethodName: {Build: func(context.Context) (proto.Message, error) {
				return &rmv1.CreateProjectRequest{OrganizationId: org.GetId(), Name: "n" + ids.New(ids.Project)[4:20]}, nil
			}},
			rmv1.ProjectService_UpdateProject_FullMethodName:        static(&rmv1.UpdateProjectRequest{ProjectId: prj.GetId(), UpdateMask: mask("description"), Description: "x"}),
			rmv1.ProjectService_ListAccessBindings_FullMethodName:   static(&accessv1.ListAccessBindingsRequest{ResourceId: prj.GetId()}),
			rmv1.ProjectService_UpdateAccessBindings_FullMethodName: static(&accessv1.UpdateAccessBindingsRequest{ResourceId: prj.GetId(), AccessBindingDeltas: addViewer}),
			rmv1.ProjectService_GetQuotas_FullMethodName:            static(&rmv1.GetQuotasRequest{ProjectId: prj.GetId()}),
			rmv1.ProjectService_DeleteProject_FullMethodName: {Build: func(context.Context) (proto.Message, error) {
				op, err := e.projects.CreateProject(rootCtx, &rmv1.CreateProjectRequest{OrganizationId: org.GetId(), Name: "d" + ids.New(ids.Project)[4:20]})
				if err != nil {
					return nil, err
				}
				p := &rmv1.Project{}
				if err := op.GetResponse().UnmarshalTo(p); err != nil {
					return nil, err
				}
				return &rmv1.DeleteProjectRequest{ProjectId: p.GetId()}, nil
			}},
		},
	})
}
