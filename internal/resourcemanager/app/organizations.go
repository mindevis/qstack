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
	"github.com/mindevis/qstack/pkg/db"
	"github.com/mindevis/qstack/pkg/events"
	"github.com/mindevis/qstack/pkg/ids"
	"github.com/mindevis/qstack/pkg/operation"
)

// GetOrganization returns a live organization or NOT_FOUND.
func (a *App) GetOrganization(ctx context.Context, id string) (domain.Organization, error) {
	o, err := store.New(a.cfg.Pool).GetOrganization(ctx, id, false)
	return o, apiError(err, "organization", id)
}

// ListOrganizationsOf returns organizations in which one of subjects holds
// a binding; the caller filters them by permission.
func (a *App) ListOrganizationsOf(ctx context.Context, subjects []string, q store.ListQuery) ([]domain.Organization, error) {
	return store.New(a.cfg.Pool).ListOrganizationsOf(ctx, subjects, q)
}

// CreateOrganization creates an organization for sign-up with its owner
// binding. A repeat with the same name and owner returns the existing
// organization; the name held by another owner is ALREADY_EXISTS.
func (a *App) CreateOrganization(ctx context.Context, name, displayName, owner string, status domain.OrganizationStatus) (domain.Organization, error) {
	now := a.now()
	o, err := domain.NewOrganization(a.cfg.IDs.New(ids.Organization), name, displayName, owner, status, now)
	if err != nil {
		return domain.Organization{}, apiError(err, "organization", "")
	}
	ownerBinding := domain.Binding{RoleID: domain.OwnerRole, Subject: domain.Subject{Type: domain.SubjectUser, ID: owner}}
	err = db.InTx(ctx, a.cfg.Pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		s := store.New(tx)
		if err := s.InsertOrganization(ctx, o); err != nil {
			return err
		}
		ch, err := domain.Replace(domain.ResourceOrganization, nil, []domain.Binding{ownerBinding}, a.newBindingID)
		if err != nil {
			return err
		}
		h := store.Holder{Type: domain.ResourceOrganization, ID: o.ID, OrganizationID: o.ID}
		if err := s.ApplyBindings(ctx, h, ch, now); err != nil {
			return err
		}
		if err := a.emit(ctx, tx, resOrganization, events.Created, o.ID, "", o.Version, OrganizationProto(o)); err != nil {
			return err
		}
		return a.emitBindings(ctx, tx, h, o.Version, ch.Result)
	})
	if errors.Is(err, store.ErrNameTaken) {
		existing, gerr := store.New(a.cfg.Pool).GetOrganizationByName(ctx, name)
		if gerr == nil && existing.OwnerUserID == owner {
			return existing, nil
		}
		return domain.Organization{}, apierr.NameConflict("organization", name)
	}
	if err != nil {
		return domain.Organization{}, apiError(err, "organization", o.ID)
	}
	return o, nil
}

// UpdateOrganization changes an organization (REQ-PLT-022, 030).
func (a *App) UpdateOrganization(ctx context.Context, subject, id, etag string, ch domain.OrganizationChanges) (*operationv1.Operation, error) {
	op, err := a.change(ctx, operation.Spec{
		ResourceID: id, Description: "Update organization", CreatedBy: subject,
		Metadata: &rmv1.UpdateOrganizationMetadata{OrganizationId: id},
	}, func(tx pgx.Tx, s *store.Store) (proto.Message, error) {
		o, err := s.GetOrganization(ctx, id, true)
		if err != nil {
			return nil, err
		}
		if err := domain.CheckEtag(o.ID, o.Version, etag); err != nil {
			return nil, err
		}
		prev := o.Version
		changed, err := o.Update(ch, a.now())
		if err != nil || !changed {
			return OrganizationProto(o), err
		}
		if err := s.UpdateOrganization(ctx, o, prev); err != nil {
			return nil, err
		}
		pb := OrganizationProto(o)
		return pb, a.emit(ctx, tx, resOrganization, events.Updated, o.ID, "", o.Version, pb)
	})
	return op, apiError(err, "organization", id)
}
