package app

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	accessv1 "github.com/mindevis/qstack/gen/go/qstack/access/v1"
	operationv1 "github.com/mindevis/qstack/gen/go/qstack/operation/v1"
	rminternalv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/internalv1"
	"github.com/mindevis/qstack/internal/resourcemanager/domain"
	"github.com/mindevis/qstack/internal/resourcemanager/store"
	"github.com/mindevis/qstack/pkg/events"
	"github.com/mindevis/qstack/pkg/ids"
	"github.com/mindevis/qstack/pkg/operation"
)

func (a *App) newBindingID() string { return a.cfg.IDs.New(ids.AccessBinding) }

// ListBindings returns up to limit bindings of a resource after afterID.
func (a *App) ListBindings(ctx context.Context, resourceID, afterID string, limit int) ([]domain.Binding, error) {
	return store.New(a.cfg.Pool).ListBindingsPage(ctx, resourceID, afterID, limit)
}

// UpdateBindings applies deltas to the bindings of an organization or a
// project; the resource's etag changes with them.
func (a *App) UpdateBindings(ctx context.Context, subject string, h store.Holder, deltas []domain.Delta) (*operationv1.Operation, error) {
	return a.changeBindings(ctx, subject, h, "Update access bindings", &accessv1.UpdateAccessBindingsMetadata{ResourceId: h.ID},
		"", func(current []domain.Binding) (domain.BindingChange, error) {
			return domain.ApplyDeltas(h.Type, current, deltas, a.newBindingID)
		})
}

// SetBindings replaces the bindings of a resource if etag is current.
func (a *App) SetBindings(ctx context.Context, subject string, h store.Holder, etag string, desired []domain.Binding) (*operationv1.Operation, error) {
	return a.changeBindings(ctx, subject, h, "Set access bindings", &accessv1.SetAccessBindingsMetadata{ResourceId: h.ID},
		etag, func(current []domain.Binding) (domain.BindingChange, error) {
			return domain.Replace(h.Type, current, desired, a.newBindingID)
		})
}

func (a *App) changeBindings(ctx context.Context, subject string, h store.Holder, desc string, meta proto.Message,
	etag string, change func([]domain.Binding) (domain.BindingChange, error),
) (*operationv1.Operation, error) {
	sp := operation.Spec{ResourceID: h.ID, Description: desc, CreatedBy: subject, Metadata: meta}
	if h.Type == domain.ResourceProject {
		sp.ProjectID = h.ID
	}
	op, err := a.change(ctx, sp, func(tx pgx.Tx, s *store.Store) (proto.Message, error) {
		// Lock the holder: binding changes of one resource are serialized
		// and bump its version (the etag of SetAccessBindings).
		touch, err := a.lockHolder(ctx, tx, s, h, etag)
		if err != nil {
			return nil, err
		}
		current, err := s.ListBindings(ctx, h.ID)
		if err != nil {
			return nil, err
		}
		ch, err := change(current)
		if err != nil || !ch.Changed() {
			return nil, err
		}
		now := a.now()
		if err := s.ApplyBindings(ctx, h, ch, now); err != nil {
			return nil, err
		}
		version, err := touch(now)
		if err != nil {
			return nil, err
		}
		return nil, a.emitBindings(ctx, tx, h, version, ch.Result)
	})
	return op, apiError(err, h.Type, h.ID)
}

// lockHolder locks the resource, checks etag and returns a function that
// bumps its version and emits its updated event.
func (a *App) lockHolder(ctx context.Context, tx pgx.Tx, s *store.Store, h store.Holder, etag string) (func(now time.Time) (int64, error), error) {
	if h.Type == domain.ResourceOrganization {
		o, err := s.GetOrganization(ctx, h.ID, true)
		if err != nil {
			return nil, err
		}
		if err := domain.CheckEtag(o.ID, o.Version, etag); err != nil {
			return nil, err
		}
		return func(now time.Time) (int64, error) {
			prev := o.Version
			o.Version++
			o.UpdatedAt = now
			if err := s.UpdateOrganization(ctx, o, prev); err != nil {
				return 0, err
			}
			return o.Version, a.emit(ctx, tx, resOrganization, events.Updated, o.ID, "", o.Version, OrganizationProto(o))
		}, nil
	}
	p, err := s.GetProject(ctx, h.ID, true)
	if err != nil {
		return nil, err
	}
	if err := domain.CheckEtag(p.ID, p.Version, etag); err != nil {
		return nil, err
	}
	return func(now time.Time) (int64, error) {
		prev := p.Version
		p.Version++
		p.UpdatedAt = now
		if err := s.UpdateProject(ctx, p, prev); err != nil {
			return 0, err
		}
		return p.Version, a.emit(ctx, tx, resProject, events.Updated, p.ID, p.ID, p.Version, ProjectProto(p))
	}, nil
}

// emitBindings publishes the full binding set of h for the IAM replica.
func (a *App) emitBindings(ctx context.Context, tx pgx.Tx, h store.Holder, version int64, bs []domain.Binding) error {
	projectID := ""
	if h.Type == domain.ResourceProject {
		projectID = h.ID
	}
	return a.emit(ctx, tx, resBindings, events.Updated, h.ID, projectID, version, &rminternalv1.AccessBindingsChanged{
		ResourceType:   h.Type,
		ResourceId:     h.ID,
		OrganizationId: h.OrganizationID,
		Version:        version,
		AccessBindings: BindingsProto(bs),
	})
}
