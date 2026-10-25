// Package app holds the Resource Manager use cases. Every change runs in
// one transaction: the Operation is created first (REQ-PLT-043, idempotency
// keys), then the resource changes, its events go to the outbox
// (REQ-PLT-090) and the Operation completes — there is no external work, so
// mutating calls return done operations (REQ-PLT-040).
package app

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	apipb "github.com/mindevis/qstack/gen/go/qstack/api"
	operationv1 "github.com/mindevis/qstack/gen/go/qstack/operation/v1"
	"github.com/mindevis/qstack/internal/resourcemanager/domain"
	"github.com/mindevis/qstack/internal/resourcemanager/store"
	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/db"
	"github.com/mindevis/qstack/pkg/events"
	"github.com/mindevis/qstack/pkg/ids"
	"github.com/mindevis/qstack/pkg/operation"
	"github.com/mindevis/qstack/pkg/outbox"
)

// Source is the event source: Resource Manager is a global service.
var Source = events.Source{Region: "global", Service: "resourcemanager"}

// Event resource names (qstack.resourcemanager.<resource>.<change>).
const (
	resOrganization = "organization"
	resProject      = "project"
	resBindings     = "accessbindings"
)

// Config holds the dependencies of App.
type Config struct {
	Pool       *pgxpool.Pool
	Operations *operation.Store
	Outbox     *outbox.Writer
	IDs        *ids.Generator
	Clock      clock.Clock
}

// App implements the use cases.
type App struct{ cfg Config }

// New returns an App.
func New(cfg Config) (*App, error) {
	if cfg.Pool == nil || cfg.Operations == nil || cfg.Outbox == nil || cfg.IDs == nil || cfg.Clock == nil {
		return nil, errors.New("app: pool, operations, outbox, ids and clock are required")
	}
	return &App{cfg: cfg}, nil
}

// now is truncated to PostgreSQL's microsecond precision.
func (a *App) now() time.Time { return a.cfg.Clock.Now().UTC().Truncate(time.Microsecond) }

// change runs fn in a transaction that first creates the operation
// described by sp; fn returns the operation's response (nil: Empty).
func (a *App) change(ctx context.Context, sp operation.Spec, fn func(tx pgx.Tx, s *store.Store) (proto.Message, error)) (*operationv1.Operation, error) {
	var op *operationv1.Operation
	err := db.InTx(ctx, a.cfg.Pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var err error
		if op, err = a.cfg.Operations.Create(ctx, tx, sp); err != nil {
			return err
		}
		resp, err := fn(tx, store.New(tx))
		if err != nil {
			return err
		}
		op, err = a.cfg.Operations.Complete(ctx, tx, op.GetId(), resp)
		return err
	})
	if err != nil {
		return nil, err
	}
	return op, nil
}

func (a *App) emit(ctx context.Context, tx pgx.Tx, resource string, change events.Change, id, projectID string, version int64, data proto.Message) error {
	_, err := a.cfg.Outbox.ResourceChanged(ctx, tx, outbox.ResourceChange{
		Resource: resource, Change: change, ResourceID: id, ProjectID: projectID, Version: version, Data: data,
	})
	return err
}

// apiError maps domain and store errors to API errors about the resource
// typ/id; anything else passes through (and becomes INTERNAL).
func apiError(err error, typ, id string) error {
	var fe *domain.InvalidFieldError
	var se *domain.InvalidStateError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &fe):
		return apierr.InvalidFields(apierr.FieldViolation{Field: fe.Field, Description: fe.Description})
	case errors.As(err, &se):
		return apierr.InvalidState(se.ID, se.State, se.Action)
	case errors.Is(err, domain.ErrEtagMismatch), errors.Is(err, db.ErrConflict):
		return apierr.EtagMismatch(id)
	case errors.Is(err, domain.ErrLastOwner):
		return apierr.New(codes.FailedPrecondition, apipb.ErrorReason_QS_INVALID_STATE, err.Error(), map[string]string{"resource_id": id})
	case errors.Is(err, store.ErrNotFound):
		return apierr.NotFound(typ, id)
	}
	return err
}
