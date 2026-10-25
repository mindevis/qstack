package domain_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mindevis/qstack/internal/resourcemanager/domain"
)

var (
	t0    = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	owner = "usr-01m5hqpzg00000000000000000"
	bob   = "usr-01m5hqpzg00000000000000001"
)

func activeOrg(t *testing.T) domain.Organization {
	t.Helper()
	o, err := domain.NewOrganization("org-01m5hqpzg00000000000000000", "acme", "ACME", owner, domain.OrgActive, t0)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func fieldOf(err error) string {
	var fe *domain.InvalidFieldError
	if errors.As(err, &fe) {
		return fe.Field
	}
	return ""
}

func TestNewOrganization(t *testing.T) {
	for _, tc := range []struct {
		name, orgName string
		status        domain.OrganizationStatus
		field         string
	}{
		{"active", "acme", domain.OrgActive, ""},
		{"pending approval", "acme-2", domain.OrgPendingApproval, ""},
		{"bad name", "Acme", domain.OrgActive, "name"},
		{"trailing dash", "acme-", domain.OrgActive, "name"},
		{"rejected on create", "acme", domain.OrgRejected, "status"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.NewOrganization("org-01m5hqpzg00000000000000000", tc.orgName, "", owner, tc.status, t0)
			if got := fieldOf(err); got != tc.field {
				t.Errorf("invalid field = %q (%v), want %q", got, err, tc.field)
			}
		})
	}
}

// REQ-PLT-030: Update applies only masked fields; unknown paths fail.
func TestOrganizationUpdate(t *testing.T) {
	o := activeOrg(t)
	changed, err := o.Update(domain.OrganizationChanges{
		Mask: []string{"description", "settings.iac_tool"}, Description: "d", DisplayName: "ignored", IacTool: domain.IacTerraform,
	}, t0.Add(time.Minute))
	if err != nil || !changed {
		t.Fatalf("Update = %v, %v", changed, err)
	}
	if o.Description != "d" || o.DisplayName != "ACME" || o.IacTool != domain.IacTerraform || o.Version != 2 {
		t.Errorf("after update: %+v", o)
	}
	if changed, err := o.Update(domain.OrganizationChanges{Mask: []string{"description"}, Description: "d"}, t0); changed || err != nil {
		t.Errorf("no-op update = %v, %v; want unchanged", changed, err)
	}
	for _, mask := range [][]string{nil, {"name"}, {"owner_user_id"}} {
		if _, err := o.Update(domain.OrganizationChanges{Mask: mask}, t0); fieldOf(err) != "update_mask" {
			t.Errorf("mask %v: %v, want invalid update_mask", mask, err)
		}
	}
	if o.Version != 2 {
		t.Errorf("failed updates changed the version to %d", o.Version)
	}
}

func TestProjectLifecycle(t *testing.T) {
	org := activeOrg(t)
	p, err := domain.NewProject("prj-01m5hqpzg00000000000000000", &org, "web", "", map[string]string{"env": "prod"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != domain.ProjectActive || p.Version != 1 {
		t.Fatalf("new project: %+v", p)
	}
	etag := p.Etag()
	if changed, err := p.Update(domain.ProjectChanges{Mask: []string{"name", "labels"}, Name: "api"}, t0); err != nil || !changed {
		t.Fatalf("Update = %v, %v", changed, err)
	}
	if p.Name != "api" || len(p.Labels) != 0 || p.Etag() == etag {
		t.Errorf("after update: %+v", p)
	}
	if _, err := p.Update(domain.ProjectChanges{Mask: []string{"name"}, Name: "API"}, t0); fieldOf(err) != "name" {
		t.Errorf("bad name: %v", err)
	}
	if err := p.Delete(t0); err != nil || p.Status != domain.ProjectDeleted {
		t.Fatalf("Delete = %v, status %s", err, p.Status)
	}
	var se *domain.InvalidStateError
	if err := p.Delete(t0); !errors.As(err, &se) {
		t.Errorf("second delete: %v, want invalid state", err)
	}
	if _, err := p.Update(domain.ProjectChanges{Mask: []string{"description"}}, t0); !errors.As(err, &se) {
		t.Errorf("update deleted: %v, want invalid state", err)
	}
}

func TestProjectsOnlyInActiveOrganizations(t *testing.T) {
	org, err := domain.NewOrganization("org-01m5hqpzg00000000000000000", "acme", "", owner, domain.OrgPendingApproval, t0)
	if err != nil {
		t.Fatal(err)
	}
	var se *domain.InvalidStateError
	if _, err := domain.NewProject("prj-01m5hqpzg00000000000000000", &org, "web", "", nil, t0); !errors.As(err, &se) {
		t.Errorf("project in pending organization: %v, want invalid state", err)
	}
}

func TestLabels(t *testing.T) {
	many := map[string]string{}
	for i := range 65 {
		many[fmt.Sprintf("k%d", i)] = ""
	}
	for _, tc := range []struct {
		name   string
		labels map[string]string
		ok     bool
	}{
		{"empty", nil, true},
		{"valid", map[string]string{"env": "prod", "team.io/a_b-c": ""}, true},
		{"uppercase key", map[string]string{"Env": "x"}, false},
		{"long value", map[string]string{"env": strings.Repeat("x", 64)}, false},
		{"too many", many, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := domain.ValidateLabels(tc.labels); (err == nil) != tc.ok {
				t.Errorf("ValidateLabels = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

// REQ-PLT-022: the etag is opaque, changes with the version, and an empty
// expected etag skips the check.
func TestEtag(t *testing.T) {
	id := "prj-01m5hqpzg00000000000000000"
	if domain.Etag(id, 1) == domain.Etag(id, 2) || domain.Etag(id, 1) == domain.Etag("prj-01m5hqpzg00000000000000001", 1) {
		t.Error("etags collide")
	}
	if err := domain.CheckEtag(id, 1, ""); err != nil {
		t.Errorf("empty etag: %v", err)
	}
	if err := domain.CheckEtag(id, 1, domain.Etag(id, 1)); err != nil {
		t.Errorf("current etag: %v", err)
	}
	if err := domain.CheckEtag(id, 2, domain.Etag(id, 1)); !errors.Is(err, domain.ErrEtagMismatch) {
		t.Errorf("stale etag: %v", err)
	}
}

func counter() func() string {
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("bnd-%026d", n)
	}
}

func user(role, id string) domain.Binding {
	return domain.Binding{RoleID: role, Subject: domain.Subject{Type: domain.SubjectUser, ID: id}}
}

func TestApplyDeltas(t *testing.T) {
	newID := counter()
	current := []domain.Binding{{ID: "bnd-a", RoleID: domain.OwnerRole, Subject: domain.Subject{Type: domain.SubjectUser, ID: owner}}}

	ch, err := domain.ApplyDeltas(domain.ResourceOrganization, current, []domain.Delta{
		{Action: domain.DeltaAdd, Binding: user("editor", bob)},
		{Action: domain.DeltaAdd, Binding: user("editor", bob)},           // duplicate: no-op
		{Action: domain.DeltaRemove, Binding: user("viewer", bob)},        // absent: no-op
		{Action: domain.DeltaAdd, Binding: user(domain.OwnerRole, owner)}, // present: keeps its ID
	}, newID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Added) != 1 || len(ch.Removed) != 0 || len(ch.Result) != 2 || ch.Added[0].ID == "" {
		t.Fatalf("change = %+v", ch)
	}
	if !ch.Changed() {
		t.Error("Changed() = false")
	}

	again, err := domain.ApplyDeltas(domain.ResourceOrganization, ch.Result, []domain.Delta{
		{Action: domain.DeltaAdd, Binding: user("editor", bob)},
	}, newID)
	if err != nil || again.Changed() {
		t.Errorf("repeat = %+v, %v; want no change", again, err)
	}
}

// Spec 03 §2: the owner role is not revocable for the last owner.
func TestLastOwner(t *testing.T) {
	current := []domain.Binding{{ID: "bnd-a", RoleID: domain.OwnerRole, Subject: domain.Subject{Type: domain.SubjectUser, ID: owner}}}
	_, err := domain.ApplyDeltas(domain.ResourceOrganization, current, []domain.Delta{
		{Action: domain.DeltaRemove, Binding: user(domain.OwnerRole, owner)},
	}, counter())
	if !errors.Is(err, domain.ErrLastOwner) {
		t.Errorf("remove last owner: %v, want ErrLastOwner", err)
	}
	if _, err := domain.Replace(domain.ResourceOrganization, current, []domain.Binding{user("admin", owner)}, counter()); !errors.Is(err, domain.ErrLastOwner) {
		t.Errorf("replace without owner: %v, want ErrLastOwner", err)
	}
	ch, err := domain.ApplyDeltas(domain.ResourceOrganization, current, []domain.Delta{
		{Action: domain.DeltaAdd, Binding: user(domain.OwnerRole, bob)},
		{Action: domain.DeltaRemove, Binding: user(domain.OwnerRole, owner)},
	}, counter())
	if err != nil || len(ch.Result) != 1 || ch.Result[0].Subject.ID != bob {
		t.Errorf("hand over ownership = %+v, %v", ch, err)
	}
}

func TestBindingValidation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resource string
		b        domain.Binding
		field    string
	}{
		{"user", domain.ResourceProject, user("compute.editor", bob), ""},
		{"all users viewer", domain.ResourceProject, domain.Binding{RoleID: "viewer", Subject: domain.Subject{Type: domain.SubjectSystem, ID: domain.AllAuthenticatedUsers}}, ""},
		{"owner on project", domain.ResourceProject, user(domain.OwnerRole, bob), "access_bindings[0].role_id"},
		{"owner to all users", domain.ResourceOrganization, domain.Binding{RoleID: domain.OwnerRole, Subject: domain.Subject{Type: domain.SubjectSystem, ID: domain.AllAuthenticatedUsers}}, "access_bindings[0].subject"},
		{"id of another type", domain.ResourceProject, domain.Binding{RoleID: "viewer", Subject: domain.Subject{Type: domain.SubjectGroup, ID: bob}}, "access_bindings[0].subject.id"},
		{"unknown system subject", domain.ResourceProject, domain.Binding{RoleID: "viewer", Subject: domain.Subject{Type: domain.SubjectSystem, ID: "allUsers"}}, "access_bindings[0].subject.id"},
		{"no type", domain.ResourceProject, domain.Binding{RoleID: "viewer", Subject: domain.Subject{ID: bob}}, "access_bindings[0].subject.type"},
		{"bad role", domain.ResourceProject, user("Editor", bob), "access_bindings[0].role_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.Replace(tc.resource, nil, []domain.Binding{tc.b}, counter())
			if got := fieldOf(err); got != tc.field {
				t.Errorf("invalid field = %q (%v), want %q", got, err, tc.field)
			}
		})
	}
}
