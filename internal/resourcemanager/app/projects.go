package app

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	operationv1 "github.com/mindevis/qstack/gen/go/qstack/operation/v1"
	rmv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/v1"
	"github.com/mindevis/qstack/internal/resourcemanager/domain"
	"github.com/mindevis/qstack/internal/resourcemanager/store"
	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/events"
	"github.com/mindevis/qstack/pkg/ids"
	"github.com/mindevis/qstack/pkg/operation"
)

// GetProject returns a live project or NOT_FOUND.
func (a *App) GetProject(ctx context.Context, id string) (domain.Project, error) {
	p, err := store.New(a.cfg.Pool).GetProject(ctx, id, false)
	return p, apiError(err, "project", id)
}

// ListProjects returns a page of the live projects of an organization.
func (a *App) ListProjects(ctx context.Context, orgID string, q store.ListQuery) ([]domain.Project, error) {
	return store.New(a.cfg.Pool).ListProjects(ctx, orgID, q)
}

// ProjectInput is a new project.
type ProjectInput struct {
	OrganizationID, Name, Description string
	Labels                            map[string]string
}

// CreateProject creates a project in an active organization.
func (a *App) CreateProject(ctx context.Context, subject string, in ProjectInput) (*operationv1.Operation, error) {
	id := a.cfg.IDs.New(ids.Project)
	op, err := a.change(ctx, operation.Spec{
		ProjectID: id, ResourceID: id, Description: "Create project", CreatedBy: subject,
		Metadata: &rmv1.CreateProjectMetadata{ProjectId: id},
	}, func(tx pgx.Tx, s *store.Store) (proto.Message, error) {
		// Shares the row with UpdateOrganization: a project cannot appear
		// in an organization that is being suspended meanwhile.
		org, err := s.GetOrganization(ctx, in.OrganizationID, true)
		if err != nil {
			return nil, err
		}
		p, err := domain.NewProject(id, &org, in.Name, in.Description, in.Labels, a.now())
		if err != nil {
			return nil, err
		}
		if err := s.InsertProject(ctx, p); err != nil {
			return nil, err
		}
		pb := ProjectProto(p)
		return pb, a.emit(ctx, tx, resProject, events.Created, p.ID, p.ID, p.Version, pb)
	})
	if errors.Is(err, store.ErrNameTaken) {
		return nil, apierr.NameConflict("project", in.Name)
	}
	if errors.Is(err, store.ErrNotFound) {
		return nil, apierr.NotFound("organization", in.OrganizationID)
	}
	return op, apiError(err, "project", id)
}

// UpdateProject changes a project (REQ-PLT-022, 030).
func (a *App) UpdateProject(ctx context.Context, subject, id, etag string, ch domain.ProjectChanges) (*operationv1.Operation, error) {
	op, err := a.change(ctx, operation.Spec{
		ProjectID: id, ResourceID: id, Description: "Update project", CreatedBy: subject,
		Metadata: &rmv1.UpdateProjectMetadata{ProjectId: id},
	}, func(tx pgx.Tx, s *store.Store) (proto.Message, error) {
		p, err := s.GetProject(ctx, id, true)
		if err != nil {
			return nil, err
		}
		if err := domain.CheckEtag(p.ID, p.Version, etag); err != nil {
			return nil, err
		}
		prev := p.Version
		changed, err := p.Update(ch, a.now())
		if err != nil || !changed {
			return ProjectProto(p), err
		}
		if err := s.UpdateProject(ctx, p, prev); err != nil {
			return nil, err
		}
		pb := ProjectProto(p)
		return pb, a.emit(ctx, tx, resProject, events.Updated, p.ID, p.ID, p.Version, pb)
	})
	if errors.Is(err, store.ErrNameTaken) {
		return nil, apierr.NameConflict("project", ch.Name)
	}
	return op, apiError(err, "project", id)
}

// DeleteProject deletes a project and its access bindings. Whether the
// project is empty is checked with quota usage in T-1.3.
func (a *App) DeleteProject(ctx context.Context, subject, id, etag string) (*operationv1.Operation, error) {
	op, err := a.change(ctx, operation.Spec{
		ProjectID: id, ResourceID: id, Description: "Delete project", CreatedBy: subject,
		Metadata: &rmv1.DeleteProjectMetadata{ProjectId: id},
	}, func(tx pgx.Tx, s *store.Store) (proto.Message, error) {
		p, err := s.GetProject(ctx, id, true)
		if err != nil {
			return nil, err
		}
		if err := domain.CheckEtag(p.ID, p.Version, etag); err != nil {
			return nil, err
		}
		before := ProjectProto(p)
		prev := p.Version
		if err := p.Delete(a.now()); err != nil {
			return nil, err
		}
		if err := s.UpdateProject(ctx, p, prev); err != nil {
			return nil, err
		}
		if err := s.DeleteBindings(ctx, p.ID); err != nil {
			return nil, err
		}
		return nil, a.emit(ctx, tx, resProject, events.Deleted, p.ID, p.ID, p.Version, before)
	})
	return op, apiError(err, "project", id)
}
