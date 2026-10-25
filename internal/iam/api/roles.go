// Package api serves the IAM gRPC API (spec 03 §6).
package api

import (
	"context"
	"slices"
	"strings"

	iamv1 "github.com/mindevis/qstack/gen/go/qstack/iam/v1"
	"github.com/mindevis/qstack/internal/iam/catalog"
	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/authz"
	"github.com/mindevis/qstack/pkg/filter"
	"github.com/mindevis/qstack/pkg/pagination"
)

// RolesPath is the resource RoleService authorizes on: the platform's role
// catalog, readable by every authenticated subject through a system
// binding of IAM (T-1.8), like the public project prj-public.
var RolesPath = authz.Path{{Type: "catalog", ID: "roles"}}

var roleFilter = filter.Schema{
	"id":    {Column: "id"},
	"stage": {Column: "stage", Values: []string{string(catalog.GA), string(catalog.Beta)}},
}

// RoleService reads the role catalog.
type RoleService struct {
	iamv1.UnimplementedRoleServiceServer
	catalog *catalog.Catalog
	pages   *pagination.Signer
}

// NewRoleService returns the RoleService of cat.
func NewRoleService(cat *catalog.Catalog, pages *pagination.Signer) *RoleService {
	return &RoleService{catalog: cat, pages: pages}
}

// GetRole returns a role.
func (s *RoleService) GetRole(ctx context.Context, req *iamv1.GetRoleRequest) (*iamv1.Role, error) {
	if err := authz.Check(ctx, RolesPath); err != nil {
		return nil, err
	}
	r, ok := s.catalog.Role(req.GetRoleId())
	if !ok {
		return nil, apierr.NotFound("role", req.GetRoleId())
	}
	return roleProto(r), nil
}

// ListRoles lists the roles ordered by ID.
func (s *RoleService) ListRoles(ctx context.Context, req *iamv1.ListRolesRequest) (*iamv1.ListRolesResponse, error) {
	size, err := pagination.PageSize(req.GetPageSize())
	if err != nil {
		return nil, err
	}
	f, err := filter.Parse(req.GetFilter(), roleFilter)
	if err != nil {
		return nil, err
	}
	after := ""
	if req.GetPageToken() != "" {
		cursor, err := s.pages.Decode(req.GetPageToken(), pagination.Binding(req))
		if err != nil {
			return nil, err
		}
		if len(cursor) != 1 {
			return nil, apierr.InvalidFields(apierr.FieldViolation{Field: "page_token", Description: "invalid page token"})
		}
		after = cursor[0]
	}
	if err := authz.Check(ctx, RolesPath); err != nil {
		return nil, err
	}
	var page []catalog.Role
	for _, r := range s.catalog.Roles() {
		if r.ID > after && matches(f, r) {
			page = append(page, r)
		}
	}
	page, more := pagination.Trim(page, size)
	resp := &iamv1.ListRolesResponse{Roles: make([]*iamv1.Role, len(page))}
	for i, r := range page {
		resp.Roles[i] = roleProto(r)
	}
	if more {
		resp.NextPageToken, err = s.pages.Encode(pagination.Binding(req), []string{page[len(page)-1].ID})
	}
	return resp, err
}

// matches evaluates a parsed filter on a role.
func matches(f filter.Filter, r catalog.Role) bool {
	for _, t := range f.Terms {
		v := r.ID
		if t.Column == "stage" {
			v = string(r.Stage)
		}
		var ok bool
		switch t.Op {
		case filter.Eq, filter.In:
			ok = slices.Contains(t.Values, v)
		case filter.NotEq:
			ok = !slices.Contains(t.Values, v)
		}
		if !ok {
			return false
		}
	}
	return true
}

var stages = map[catalog.Stage]iamv1.Role_Stage{catalog.GA: iamv1.Role_STAGE_GA, catalog.Beta: iamv1.Role_STAGE_BETA}

func roleProto(r catalog.Role) *iamv1.Role {
	return &iamv1.Role{
		Id: r.ID, Title: r.Title, Description: strings.TrimSpace(r.Description),
		Permissions: slices.Clone(r.Permissions), Stage: stages[r.Stage],
	}
}
