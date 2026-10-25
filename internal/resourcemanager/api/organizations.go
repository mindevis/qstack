package api

import (
	"context"

	accessv1 "github.com/mindevis/qstack/gen/go/qstack/access/v1"
	operationv1 "github.com/mindevis/qstack/gen/go/qstack/operation/v1"
	rminternalv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/internalv1"
	rmv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/v1"
	"github.com/mindevis/qstack/internal/resourcemanager/app"
	"github.com/mindevis/qstack/internal/resourcemanager/domain"
	"github.com/mindevis/qstack/internal/resourcemanager/store"
	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/authz"
	"github.com/mindevis/qstack/pkg/ids"
	"github.com/mindevis/qstack/pkg/pagination"
)

const orgGet = "resourcemanager.organizations.get"

func orgPath(id string) authz.Path { return authz.Path{{Type: "organization", ID: id}} }

type organizations struct {
	rmv1.UnimplementedOrganizationServiceServer
	*handlers
}

// loadOrg loads an organization and authorizes the method on it; NOT_FOUND
// if the caller may not see it (REQ-SEC-004).
func (s *organizations) loadOrg(ctx context.Context, id string) (domain.Organization, error) {
	o, err := s.app.GetOrganization(ctx, id)
	if err != nil {
		return domain.Organization{}, err
	}
	return o, authz.CheckResource(ctx, orgPath(o.ID), orgGet)
}

func (s *organizations) GetOrganization(ctx context.Context, req *rmv1.GetOrganizationRequest) (*rmv1.Organization, error) {
	o, err := s.loadOrg(ctx, req.GetOrganizationId())
	if err != nil {
		return nil, err
	}
	return app.OrganizationProto(o), nil
}

// ListOrganizations returns the organizations in which the caller holds a
// binding and may list (spec 03 §6: "available to the subject").
func (s *organizations) ListOrganizations(ctx context.Context, req *rmv1.ListOrganizationsRequest) (*rmv1.ListOrganizationsResponse, error) {
	p, err := s.listPage(req, req.GetPageSize(), req.GetPageToken(), req.GetFilter(), "", store.OrganizationFilter)
	if err != nil {
		return nil, err
	}
	orgs, err := s.app.ListOrganizationsOf(ctx, []string{subject(ctx)}, p.q)
	if err != nil {
		return nil, err
	}
	orgs, more := pagination.Trim(orgs, p.size)
	paths := make([]authz.Path, len(orgs))
	for i, o := range orgs {
		paths[i] = orgPath(o.ID)
	}
	allowed, err := authz.Filter(ctx, paths)
	if err != nil {
		return nil, err
	}
	resp := &rmv1.ListOrganizationsResponse{}
	for i, o := range orgs {
		if allowed[i] {
			resp.Organizations = append(resp.Organizations, app.OrganizationProto(o))
		}
	}
	if len(orgs) > 0 {
		last := orgs[len(orgs)-1]
		resp.NextPageToken, err = s.nextToken(req, more, store.Cursor(p.q.Order, last.ID, last.Name, last.CreatedAt))
	}
	return resp, err
}

func (s *organizations) UpdateOrganization(ctx context.Context, req *rmv1.UpdateOrganizationRequest) (*operationv1.Operation, error) {
	o, err := s.loadOrg(ctx, req.GetOrganizationId())
	if err != nil {
		return nil, err
	}
	return s.app.UpdateOrganization(ctx, subject(ctx), o.ID, req.GetEtag(), domain.OrganizationChanges{
		Mask:        req.GetUpdateMask().GetPaths(),
		DisplayName: req.GetDisplayName(),
		Description: req.GetDescription(),
		Labels:      req.GetLabels(),
		IacTool:     app.IacToolOf(req.GetSettings().GetIacTool()),
	})
}

func (s *organizations) ListAccessBindings(ctx context.Context, req *accessv1.ListAccessBindingsRequest) (*accessv1.ListAccessBindingsResponse, error) {
	if err := expectKind(req.GetResourceId(), ids.Organization); err != nil {
		return nil, err
	}
	o, err := s.loadOrg(ctx, req.GetResourceId())
	if err != nil {
		return nil, err
	}
	return s.listBindings(ctx, req, o.ID)
}

func (s *organizations) SetAccessBindings(ctx context.Context, req *accessv1.SetAccessBindingsRequest) (*operationv1.Operation, error) {
	if err := expectKind(req.GetResourceId(), ids.Organization); err != nil {
		return nil, err
	}
	o, err := s.loadOrg(ctx, req.GetResourceId())
	if err != nil {
		return nil, err
	}
	desired := make([]domain.Binding, len(req.GetAccessBindings()))
	for i, b := range req.GetAccessBindings() {
		desired[i] = app.BindingOf(b)
	}
	return s.app.SetBindings(ctx, subject(ctx), orgHolder(o), req.GetEtag(), desired)
}

func (s *organizations) UpdateAccessBindings(ctx context.Context, req *accessv1.UpdateAccessBindingsRequest) (*operationv1.Operation, error) {
	if err := expectKind(req.GetResourceId(), ids.Organization); err != nil {
		return nil, err
	}
	o, err := s.loadOrg(ctx, req.GetResourceId())
	if err != nil {
		return nil, err
	}
	return s.app.UpdateBindings(ctx, subject(ctx), orgHolder(o), deltas(req.GetAccessBindingDeltas()))
}

func orgHolder(o domain.Organization) store.Holder {
	return store.Holder{Type: domain.ResourceOrganization, ID: o.ID, OrganizationID: o.ID}
}

// --- shared by organizations and projects ---

func (h *handlers) listBindings(ctx context.Context, req *accessv1.ListAccessBindingsRequest, resourceID string) (*accessv1.ListAccessBindingsResponse, error) {
	size, after, err := h.bindingsPage(req, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, err
	}
	bs, err := h.app.ListBindings(ctx, resourceID, after, size+1)
	if err != nil {
		return nil, err
	}
	bs, more := pagination.Trim(bs, size)
	resp := &accessv1.ListAccessBindingsResponse{AccessBindings: app.BindingsProto(bs)}
	if len(bs) > 0 {
		resp.NextPageToken, err = h.nextToken(req, more, []string{bs[len(bs)-1].ID})
	}
	return resp, err
}

func deltas(in []*accessv1.AccessBindingDelta) []domain.Delta {
	out := make([]domain.Delta, len(in))
	for i, d := range in {
		out[i] = domain.Delta{Binding: app.BindingOf(d.GetAccessBinding())}
		switch d.GetAction() {
		case accessv1.AccessBindingDelta_ACTION_ADD:
			out[i].Action = domain.DeltaAdd
		case accessv1.AccessBindingDelta_ACTION_REMOVE:
			out[i].Action = domain.DeltaRemove
		}
	}
	return out
}

// expectKind rejects a resource_id of another type: the shared access
// messages accept any resource ID.
func expectKind(id string, k ids.Kind) error {
	if ids.ParseKind(id, k) != nil {
		return apierr.InvalidFields(apierr.FieldViolation{Field: "resource_id", Description: "must be a " + k.Name() + " id"})
	}
	return nil
}

// --- internal API ---

type internalOrganizations struct {
	rminternalv1.UnimplementedOrganizationServiceServer
	app *app.App
}

func (s *internalOrganizations) CreateOrganization(ctx context.Context, req *rminternalv1.CreateOrganizationRequest) (*rmv1.Organization, error) {
	o, err := s.app.CreateOrganization(ctx, req.GetName(), req.GetDisplayName(), req.GetOwnerUserId(), app.OrgStatusOf(req.GetStatus()))
	if err != nil {
		return nil, err
	}
	return app.OrganizationProto(o), nil
}
