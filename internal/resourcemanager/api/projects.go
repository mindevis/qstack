package api

import (
	"context"

	accessv1 "github.com/mindevis/qstack/gen/go/qstack/access/v1"
	operationv1 "github.com/mindevis/qstack/gen/go/qstack/operation/v1"
	rmv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/v1"
	"github.com/mindevis/qstack/internal/resourcemanager/app"
	"github.com/mindevis/qstack/internal/resourcemanager/domain"
	"github.com/mindevis/qstack/internal/resourcemanager/store"
	"github.com/mindevis/qstack/pkg/authz"
	"github.com/mindevis/qstack/pkg/ids"
	"github.com/mindevis/qstack/pkg/pagination"
)

const projectGet = "resourcemanager.projects.get"

type projects struct {
	rmv1.UnimplementedProjectServiceServer
	*handlers
}

// loadProject loads a project and authorizes the method on it; NOT_FOUND
// if the caller may not see it (REQ-SEC-004).
func (s *projects) loadProject(ctx context.Context, id string) (domain.Project, error) {
	p, err := s.app.GetProject(ctx, id)
	if err != nil {
		return domain.Project{}, err
	}
	return p, authz.CheckResource(ctx, authz.ProjectPath(p.OrganizationID, p.ID), projectGet)
}

// loadParent loads the organization of Create and List and authorizes the
// method on it (spec 03 §4.2).
func (s *projects) loadParent(ctx context.Context, orgID string) (domain.Organization, error) {
	o, err := s.app.GetOrganization(ctx, orgID)
	if err != nil {
		return domain.Organization{}, err
	}
	return o, authz.CheckResource(ctx, orgPath(o.ID), orgGet)
}

func (s *projects) GetProject(ctx context.Context, req *rmv1.GetProjectRequest) (*rmv1.Project, error) {
	p, err := s.loadProject(ctx, req.GetProjectId())
	if err != nil {
		return nil, err
	}
	return app.ProjectProto(p), nil
}

func (s *projects) ListProjects(ctx context.Context, req *rmv1.ListProjectsRequest) (*rmv1.ListProjectsResponse, error) {
	pg, err := s.listPage(req, req.GetPageSize(), req.GetPageToken(), req.GetFilter(), req.GetOrderBy(), store.ProjectFilter)
	if err != nil {
		return nil, err
	}
	o, err := s.loadParent(ctx, req.GetOrganizationId())
	if err != nil {
		return nil, err
	}
	ps, err := s.app.ListProjects(ctx, o.ID, pg.q)
	if err != nil {
		return nil, err
	}
	ps, more := pagination.Trim(ps, pg.size)
	resp := &rmv1.ListProjectsResponse{Projects: make([]*rmv1.Project, len(ps))}
	for i, p := range ps {
		resp.Projects[i] = app.ProjectProto(p)
	}
	if len(ps) > 0 {
		last := ps[len(ps)-1]
		resp.NextPageToken, err = s.nextToken(req, more, store.Cursor(pg.q.Order, last.ID, last.Name, last.CreatedAt))
	}
	return resp, err
}

func (s *projects) CreateProject(ctx context.Context, req *rmv1.CreateProjectRequest) (*operationv1.Operation, error) {
	o, err := s.loadParent(ctx, req.GetOrganizationId())
	if err != nil {
		return nil, err
	}
	return s.app.CreateProject(ctx, subject(ctx), app.ProjectInput{
		OrganizationID: o.ID, Name: req.GetName(), Description: req.GetDescription(), Labels: req.GetLabels(),
	})
}

func (s *projects) UpdateProject(ctx context.Context, req *rmv1.UpdateProjectRequest) (*operationv1.Operation, error) {
	p, err := s.loadProject(ctx, req.GetProjectId())
	if err != nil {
		return nil, err
	}
	return s.app.UpdateProject(ctx, subject(ctx), p.ID, req.GetEtag(), domain.ProjectChanges{
		Mask: req.GetUpdateMask().GetPaths(), Name: req.GetName(), Description: req.GetDescription(), Labels: req.GetLabels(),
	})
}

func (s *projects) DeleteProject(ctx context.Context, req *rmv1.DeleteProjectRequest) (*operationv1.Operation, error) {
	p, err := s.loadProject(ctx, req.GetProjectId())
	if err != nil {
		return nil, err
	}
	return s.app.DeleteProject(ctx, subject(ctx), p.ID, req.GetEtag())
}

func (s *projects) ListAccessBindings(ctx context.Context, req *accessv1.ListAccessBindingsRequest) (*accessv1.ListAccessBindingsResponse, error) {
	if err := expectKind(req.GetResourceId(), ids.Project); err != nil {
		return nil, err
	}
	p, err := s.loadProject(ctx, req.GetResourceId())
	if err != nil {
		return nil, err
	}
	return s.listBindings(ctx, req, p.ID)
}

func (s *projects) UpdateAccessBindings(ctx context.Context, req *accessv1.UpdateAccessBindingsRequest) (*operationv1.Operation, error) {
	if err := expectKind(req.GetResourceId(), ids.Project); err != nil {
		return nil, err
	}
	p, err := s.loadProject(ctx, req.GetResourceId())
	if err != nil {
		return nil, err
	}
	h := store.Holder{Type: domain.ResourceProject, ID: p.ID, OrganizationID: p.OrganizationID}
	return s.app.UpdateBindings(ctx, subject(ctx), h, deltas(req.GetAccessBindingDeltas()))
}
