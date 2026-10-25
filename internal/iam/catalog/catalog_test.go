package catalog_test

import (
	"strings"
	"testing"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	// Every public API package, so its descriptors are registered.
	_ "github.com/mindevis/qstack/gen/go/qstack/iam/v1"
	_ "github.com/mindevis/qstack/gen/go/qstack/operation/v1"
	_ "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/v1"
	"github.com/mindevis/qstack/internal/iam/catalog"
	"github.com/mindevis/qstack/pkg/protoopts"
)

func load(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// method permissions of every public service, with whether the method is
// a read (HTTP GET) and whether it changes access bindings.
type method struct {
	name         protoreflect.FullName
	read, access bool
}

func methods(t *testing.T) map[string][]method {
	t.Helper()
	out := map[string][]method{}
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		pkg := string(fd.Package())
		if !strings.HasPrefix(pkg, "qstack.") || strings.Contains(pkg, ".internal.") {
			return true
		}
		for i := range fd.Services().Len() {
			sd := fd.Services().Get(i)
			for j := range sd.Methods().Len() {
				md := sd.Methods().Get(j)
				perm, ok := protoopts.Permission(md)
				if !ok {
					t.Errorf("%s has no permission", md.FullName())
					continue
				}
				rule, _ := proto.GetExtension(md.Options(), annotations.E_Http).(*annotations.HttpRule)
				name := string(md.Name())
				out[perm] = append(out[perm], method{
					name:   md.FullName(),
					read:   rule.GetGet() != "",
					access: strings.HasSuffix(name, "AccessBindings") && rule.GetGet() == "",
				})
			}
		}
		return true
	})
	if len(out) < 50 {
		t.Fatalf("found only %d permissions", len(out))
	}
	return out
}

// T-1.7 acceptance: every public method's permission is in the catalog,
// with a kind that matches the method; and the catalog lists no permission
// that no method checks, except `use` permissions (authz.Require).
func TestCatalogCoversMethods(t *testing.T) {
	c := load(t)
	ms := methods(t)
	for perm, list := range ms {
		kind, ok := c.Kind(perm)
		if !ok {
			t.Errorf("%s (%s) is not in internal/iam/catalog/roles.yaml", perm, list[0].name)
			continue
		}
		for _, m := range list {
			want := catalog.Write
			switch {
			case m.read:
				want = catalog.Read
			case m.access:
				want = catalog.Access
			}
			if kind != want {
				t.Errorf("%s: kind %s, but %s needs %s", perm, kind, m.name, want)
			}
		}
	}
	for _, p := range c.Permissions() {
		if k, _ := c.Kind(p); k != catalog.Use && ms[p] == nil {
			t.Errorf("%s is in the catalog but no method declares it", p)
		}
	}
}

func TestRoles(t *testing.T) {
	c := load(t)
	for _, tc := range []struct {
		role, perm string
		want       bool
	}{
		{"viewer", "resourcemanager.projects.get", true},
		{"viewer", "resourcemanager.projects.create", false},
		{"viewer", "iam.serviceAccounts.actAs", false},
		{"editor", "resourcemanager.projects.create", true},
		{"editor", "iam.serviceAccounts.actAs", true},
		{"editor", "resourcemanager.projects.updateAccessBindings", false},
		{"admin", "resourcemanager.projects.updateAccessBindings", true},
		{"iam.viewer", "iam.users.get", true},
		{"iam.viewer", "resourcemanager.projects.get", false},
		{"resourcemanager.admin", "resourcemanager.organizations.setAccessBindings", true},
		{"resourcemanager.editor", "resourcemanager.organizations.setAccessBindings", false},
		{"iam.serviceAccounts.user", "iam.serviceAccounts.actAs", true},
		{"iam.serviceAccounts.user", "iam.serviceAccounts.delete", false},
		{"resource-manager.organizations.owner", "resourcemanager.organizations.setAccessBindings", true},
		{"unknown", "iam.users.get", false},
	} {
		if got := c.Grants(tc.role, tc.perm); got != tc.want {
			t.Errorf("Grants(%s, %s) = %v, want %v", tc.role, tc.perm, got, tc.want)
		}
	}
	owner, _ := c.Role("resource-manager.organizations.owner")
	if len(owner.Permissions) != len(c.Permissions()) {
		t.Errorf("owner has %d of %d permissions", len(owner.Permissions), len(c.Permissions()))
	}
	roles := c.Roles()
	for i := 1; i < len(roles); i++ {
		if roles[i-1].ID >= roles[i].ID {
			t.Fatalf("roles not sorted: %s, %s", roles[i-1].ID, roles[i].ID)
		}
	}
	for _, r := range roles {
		if r.Stage != catalog.GA || r.Title == "" {
			t.Errorf("role %s: stage %s, title %q", r.ID, r.Stage, r.Title)
		}
	}
	if err := c.Validate("compute.nothing"); err == nil {
		t.Error("Validate accepted an unknown role")
	}
}

func TestParseErrors(t *testing.T) {
	for _, tc := range []struct{ name, yaml string }{
		{"unknown key", "services: {}\nextra: 1\n"},
		{"unknown kind", "services:\n  a:\n    permissions:\n      x.get: maybe\n"},
		{"bad permission", "services:\n  a:\n    permissions:\n      get: read\n"},
		{"undeclared in role", "services:\n  a:\n    permissions:\n      x.get: read\nroles:\n  - id: r\n    title: R\n    permissions: [a.x.list]\n"},
		{"duplicate role", "services:\n  a:\n    permissions:\n      x.get: read\nroles:\n  - id: viewer\n    title: V\n    permissions: [a.x.get]\n"},
		{"all and permissions", "services:\n  a:\n    permissions:\n      x.get: read\nroles:\n  - id: r\n    title: R\n    all: true\n    permissions: [a.x.get]\n"},
		{"bad stage", "services:\n  a:\n    permissions:\n      x.get: read\nroles:\n  - id: r\n    title: R\n    stage: ALPHA\n    permissions: [a.x.get]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := catalog.Parse([]byte(tc.yaml)); err == nil {
				t.Error("Parse accepted it")
			}
		})
	}
}
