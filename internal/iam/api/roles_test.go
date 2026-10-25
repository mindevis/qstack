package api_test

import (
	"context"
	"log/slog"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	iamv1 "github.com/mindevis/qstack/gen/go/qstack/iam/v1"
	"github.com/mindevis/qstack/internal/iam/api"
	"github.com/mindevis/qstack/internal/iam/catalog"
	"github.com/mindevis/qstack/pkg/authz"
	"github.com/mindevis/qstack/pkg/authz/authzfake"
	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/pagination"
	"github.com/mindevis/qstack/pkg/validate"
)

func authenticate(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("x-test-subject"); len(v) == 1 {
		ctx = authz.WithSubject(ctx, authz.Subject{ID: v[0], Type: "user"})
	}
	return h(ctx, req)
}

func as(subject string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "x-test-subject", subject)
}

func newClient(t *testing.T) (iamv1.RoleServiceClient, *authzfake.AccessService) {
	t.Helper()
	cat, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	pages, err := pagination.NewSigner(clock.Real(), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	v, err := validate.New()
	if err != nil {
		t.Fatal(err)
	}
	access := authzfake.New()
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(authenticate,
		authz.UnaryServerInterceptor(authz.Options{Authorizer: access, Logger: slog.New(slog.DiscardHandler)}),
		v.UnaryServerInterceptor()))
	iamv1.RegisterRoleServiceServer(srv, api.NewRoleService(cat, pages))
	if p := authz.CheckServer(srv, nil); len(p) > 0 {
		t.Fatalf("methods without permission: %v", p)
	}
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
	return iamv1.NewRoleServiceClient(conn), access
}

const alice = "usr-01m5hqpzg00000000000000000"

// Every authenticated subject reads the catalog through a binding on
// api.RolesPath; nothing else grants it.
func TestRoleServiceAuthorization(t *testing.T) {
	c, access := newClient(t)
	req := &iamv1.GetRoleRequest{RoleId: "viewer"}
	if _, err := c.GetRole(context.Background(), req); status.Code(err) != codes.Unauthenticated {
		t.Errorf("anonymous: %v", err)
	}
	if _, err := c.GetRole(as(alice), req); status.Code(err) != codes.PermissionDenied {
		t.Errorf("without binding: %v", err)
	}
	access.Grant(authzfake.AllAuthenticatedUsers, "iam.roles.get", authz.Resource{Type: "catalog", ID: "roles"})
	access.Grant(authzfake.AllAuthenticatedUsers, "iam.roles.list", authz.Resource{Type: "catalog", ID: "roles"})
	r, err := c.GetRole(as(alice), req)
	if err != nil || r.GetId() != "viewer" || r.GetStage() != iamv1.Role_STAGE_GA || len(r.GetPermissions()) == 0 {
		t.Fatalf("GetRole = %v, %v", r, err)
	}
	if _, err := c.GetRole(as(alice), &iamv1.GetRoleRequest{RoleId: "nothing"}); status.Code(err) != codes.NotFound {
		t.Errorf("unknown role: %v", err)
	}
	if _, err := c.ListRoles(as(alice), &iamv1.ListRolesRequest{}); err != nil {
		t.Errorf("ListRoles: %v", err)
	}
}

func TestListRoles(t *testing.T) {
	c, access := newClient(t)
	access.Grant(alice, "iam.roles.list", authz.Resource{Type: "catalog", ID: "roles"})
	cat, _ := catalog.Load()

	var ids []string
	req := &iamv1.ListRolesRequest{PageSize: 4}
	for {
		resp, err := c.ListRoles(as(alice), req)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range resp.GetRoles() {
			ids = append(ids, r.GetId())
		}
		if resp.GetNextPageToken() == "" {
			break
		}
		req.PageToken = resp.GetNextPageToken()
	}
	if len(ids) != len(cat.Roles()) {
		t.Errorf("paged %d roles, catalog has %d", len(ids), len(cat.Roles()))
	}
	for i := 1; i < len(ids); i++ {
		if ids[i-1] >= ids[i] {
			t.Fatalf("not ordered by id: %v", ids)
		}
	}

	resp, err := c.ListRoles(as(alice), &iamv1.ListRolesRequest{Filter: `id IN ("viewer", "admin") AND stage = GA`})
	if err != nil || len(resp.GetRoles()) != 2 || resp.GetRoles()[0].GetId() != "admin" {
		t.Errorf("filtered = %v, %v", resp.GetRoles(), err)
	}
	if _, err := c.ListRoles(as(alice), &iamv1.ListRolesRequest{Filter: "title = x"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("unknown filter field: %v", err)
	}
}
