// Package operation stores long-running operations (LRO, spec 02 §5.3) in the
// owner service's database (REQ-PLT-042).
//
// A mutating handler creates the Operation in the same transaction as the
// resource change and returns it right away (REQ-PLT-040); the controller
// later completes it with the final resource or an error. Create also records
// the request's idempotency key (pkg/idempotency) and enforces one running
// operation per resource (REQ-PLT-043).
//
// The tables come with the package: every service runs Migrate next to its
// own migrations.
package operation

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	operationv1 "github.com/mindevis/qstack/gen/go/qstack/operation/v1"
	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/db"
	"github.com/mindevis/qstack/pkg/idempotency"
	"github.com/mindevis/qstack/pkg/ids"
)

// Retention is how long finished operations are kept (spec 02 §5.3).
const Retention = 30 * 24 * time.Hour

// VersionTable tracks this package's migrations separately from the service's.
const VersionTable = "qs_operation_db_version"

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate creates or upgrades the operations and idempotency_keys tables.
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	return db.MigrateTable(ctx, pool, sub, VersionTable)
}

// DB is satisfied by *pgxpool.Pool and pgx.Tx.
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ErrNotRunning means the operation does not exist or is already done.
var ErrNotRunning = errors.New("operation: not found or already done")

// Store reads operations from db and writes them in the caller's transaction.
type Store struct {
	db    DB
	clock clock.Clock
	ids   *ids.Generator
}

// New returns a Store; db (usually the pool) serves reads.
func New(db DB, clk clock.Clock, gen *ids.Generator) *Store {
	return &Store{db: db, clock: clk, ids: gen}
}

// Spec describes a new operation.
type Spec struct {
	// ProjectID is empty for resources outside projects (organizations).
	ProjectID  string
	ResourceID string
	// Description is a short English phrase, e.g. "Create instance".
	Description string
	// CreatedBy is the subject ID of the caller.
	CreatedBy string
	// Metadata is the method's metadata message, e.g.
	// CreateInstanceMetadata; it must hold ResourceID in a top-level string
	// field (REQ-PLT-041).
	Metadata proto.Message
}

// Create inserts a running operation in tx. Call it before the resource
// change: if the request carries an idempotency reservation, Create claims
// the key first, so that a concurrent identical request blocks here and then
// gets idempotency.ErrDuplicateRequest (return it unchanged and roll back).
//
// If another operation on the resource is running, Create returns
// FAILED_PRECONDITION / QS_RESOURCE_BUSY (REQ-PLT-043).
func (s *Store) Create(ctx context.Context, tx DB, sp Spec) (*operationv1.Operation, error) {
	if _, err := ids.Parse(sp.ResourceID); err != nil {
		return nil, fmt.Errorf("operation: resource id: %w", err)
	}
	if sp.Description == "" {
		return nil, errors.New("operation: empty description")
	}
	if sp.Metadata == nil || !holdsString(sp.Metadata.ProtoReflect(), sp.ResourceID) {
		return nil, errors.New("operation: metadata must contain the resource id (REQ-PLT-041)")
	}
	meta, err := anypb.New(sp.Metadata)
	if err != nil {
		return nil, fmt.Errorf("operation: metadata: %w", err)
	}
	metaBytes, err := proto.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("operation: metadata: %w", err)
	}
	id := s.ids.New(ids.Operation)
	now := s.now()

	if r, ok := idempotency.ReservationFrom(ctx); ok {
		// An expired key is taken over; a live one means a duplicate.
		tag, err := tx.Exec(ctx, `
			INSERT INTO idempotency_keys (subject, key, request_hash, operation_id, expire_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (subject, key) DO UPDATE
			SET request_hash = EXCLUDED.request_hash, operation_id = EXCLUDED.operation_id, expire_at = EXCLUDED.expire_at
			WHERE idempotency_keys.expire_at <= $6`,
			r.Subject, r.Key, r.RequestHash, id, now.Add(idempotency.TTL), now)
		if err != nil {
			return nil, fmt.Errorf("operation: record idempotency key: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return nil, idempotency.ErrDuplicateRequest
		}
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO operations (id, project_id, resource_id, description, created_by, metadata, create_time, modify_time)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
		ON CONFLICT (resource_id) WHERE NOT done DO NOTHING`,
		id, sp.ProjectID, sp.ResourceID, sp.Description, sp.CreatedBy, metaBytes, now)
	if err != nil {
		return nil, fmt.Errorf("operation: insert: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var running string
		err := tx.QueryRow(ctx, `SELECT id FROM operations WHERE resource_id = $1 AND NOT done`, sp.ResourceID).Scan(&running)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("operation: find running: %w", err)
		}
		return nil, apierr.ResourceBusy(sp.ResourceID, running)
	}
	return &operationv1.Operation{
		Id:          id,
		Description: sp.Description,
		CreateTime:  timestamppb.New(now),
		CreatedBy:   sp.CreatedBy,
		ModifyTime:  timestamppb.New(now),
		Metadata:    meta,
		ResourceId:  sp.ResourceID,
		ProjectId:   sp.ProjectID,
	}, nil
}

// Complete marks the operation done with response, the resource in its final
// state (nil means google.protobuf.Empty, e.g. for Delete).
func (s *Store) Complete(ctx context.Context, tx DB, id string, response proto.Message) (*operationv1.Operation, error) {
	if response == nil {
		response = &emptypb.Empty{}
	}
	a, err := anypb.New(response)
	if err != nil {
		return nil, fmt.Errorf("operation: response: %w", err)
	}
	b, err := proto.Marshal(a)
	if err != nil {
		return nil, fmt.Errorf("operation: response: %w", err)
	}
	return s.finish(ctx, tx, id, "response", b)
}

// Fail marks the operation done with cause as its error. cause should be an
// apierr error; anything else (and INTERNAL/UNKNOWN/DATA_LOSS statuses)
// becomes a generic INTERNAL error, so log the cause before calling Fail.
func (s *Store) Fail(ctx context.Context, tx DB, id string, cause error) (*operationv1.Operation, error) {
	b, err := proto.Marshal(errorStatus(cause))
	if err != nil {
		return nil, fmt.Errorf("operation: error: %w", err)
	}
	return s.finish(ctx, tx, id, "error", b)
}

func (s *Store) finish(ctx context.Context, tx DB, id, column string, value []byte) (*operationv1.Operation, error) {
	now := s.now()
	// column is one of two constants, never user input.
	op, err := scan(tx.QueryRow(ctx, `
		UPDATE operations SET done = true, `+column+` = $2, modify_time = $3, expire_at = $4
		WHERE id = $1 AND NOT done
		RETURNING `+columns, id, value, now, now.Add(Retention)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotRunning
	}
	return op, err
}

// RequestCancel flags a running operation for cancellation; the controller
// doing the work checks CancelRequested and fails the operation with
// CANCELLED if it can stop. A finished operation is returned unchanged.
func (s *Store) RequestCancel(ctx context.Context, tx DB, id string) (*operationv1.Operation, error) {
	op, err := scan(tx.QueryRow(ctx, `
		UPDATE operations SET cancel_requested = true, modify_time = $2
		WHERE id = $1 AND NOT done
		RETURNING `+columns, id, s.now()))
	if errors.Is(err, pgx.ErrNoRows) {
		return s.get(ctx, tx, id)
	}
	return op, err
}

// CancelRequested reports whether cancellation of a running operation was requested.
func (s *Store) CancelRequested(ctx context.Context, id string) (bool, error) {
	var requested bool
	err := s.db.QueryRow(ctx, `SELECT cancel_requested AND NOT done FROM operations WHERE id = $1`, id).Scan(&requested)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, apierr.NotFound("operation", id)
	}
	return requested, err
}

// Get returns the operation or NOT_FOUND.
func (s *Store) Get(ctx context.Context, id string) (*operationv1.Operation, error) {
	return s.get(ctx, s.db, id)
}

func (s *Store) get(ctx context.Context, q DB, id string) (*operationv1.Operation, error) {
	op, err := scan(q.QueryRow(ctx, `SELECT `+columns+` FROM operations WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.NotFound("operation", id)
	}
	return op, err
}

// Lookup implements idempotency.Store.
func (s *Store) Lookup(ctx context.Context, subject, key string) (idempotency.Record, bool, error) {
	var hash []byte
	var r row
	err := s.db.QueryRow(ctx, `
		SELECT k.request_hash, `+prefixedColumns+`
		FROM idempotency_keys k JOIN operations o ON o.id = k.operation_id
		WHERE k.subject = $1 AND k.key = $2 AND k.expire_at > $3`,
		subject, key, s.now()).Scan(append([]any{&hash}, r.dest()...)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return idempotency.Record{}, false, nil
	}
	if err != nil {
		return idempotency.Record{}, false, err
	}
	op, err := r.proto()
	if err != nil {
		return idempotency.Record{}, false, err
	}
	return idempotency.Record{RequestHash: hash, Operation: op}, true, nil
}

// Purge deletes up to limit finished operations past retention, with their
// idempotency keys, and all expired keys. It returns the number of
// operations deleted; call it periodically until it returns less than limit.
func (s *Store) Purge(ctx context.Context, limit int) (int64, error) {
	now := s.now()
	if _, err := s.db.Exec(ctx, `DELETE FROM idempotency_keys WHERE expire_at <= $1`, now); err != nil {
		return 0, fmt.Errorf("operation: purge keys: %w", err)
	}
	tag, err := s.db.Exec(ctx, `
		DELETE FROM operations WHERE id IN (
			SELECT id FROM operations WHERE done AND expire_at <= $1 ORDER BY expire_at LIMIT $2)`, now, limit)
	if err != nil {
		return 0, fmt.Errorf("operation: purge: %w", err)
	}
	return tag.RowsAffected(), nil
}

// now is truncated to PostgreSQL's microsecond precision so that returned
// and stored timestamps agree.
func (s *Store) now() time.Time { return s.clock.Now().UTC().Truncate(time.Microsecond) }

const columns = `id, project_id, resource_id, description, created_by, metadata, done, error, response, create_time, modify_time`

const prefixedColumns = `o.id, o.project_id, o.resource_id, o.description, o.created_by, o.metadata, o.done, o.error, o.response, o.create_time, o.modify_time`

type row struct {
	id, projectID, resourceID, description, createdBy string
	metadata, errStatus, response                     []byte
	done                                              bool
	createTime, modifyTime                            time.Time
}

func (r *row) dest() []any {
	return []any{&r.id, &r.projectID, &r.resourceID, &r.description, &r.createdBy, &r.metadata,
		&r.done, &r.errStatus, &r.response, &r.createTime, &r.modifyTime}
}

func scan(pr pgx.Row) (*operationv1.Operation, error) {
	var r row
	if err := pr.Scan(r.dest()...); err != nil {
		return nil, err
	}
	return r.proto()
}

func (r *row) proto() (*operationv1.Operation, error) {
	op := &operationv1.Operation{
		Id:          r.id,
		Description: r.description,
		CreateTime:  timestamppb.New(r.createTime),
		CreatedBy:   r.createdBy,
		ModifyTime:  timestamppb.New(r.modifyTime),
		Done:        r.done,
		Metadata:    &anypb.Any{},
		ResourceId:  r.resourceID,
		ProjectId:   r.projectID,
	}
	if err := proto.Unmarshal(r.metadata, op.Metadata); err != nil {
		return nil, fmt.Errorf("operation %s: metadata: %w", r.id, err)
	}
	switch {
	case r.errStatus != nil:
		st := &statuspb.Status{}
		if err := proto.Unmarshal(r.errStatus, st); err != nil {
			return nil, fmt.Errorf("operation %s: error: %w", r.id, err)
		}
		op.Result = &operationv1.Operation_Error{Error: st}
	case r.response != nil:
		a := &anypb.Any{}
		if err := proto.Unmarshal(r.response, a); err != nil {
			return nil, fmt.Errorf("operation %s: response: %w", r.id, err)
		}
		op.Result = &operationv1.Operation_Response{Response: a}
	}
	return op, nil
}

// errorStatus converts cause into the operation's error, hiding unexpected
// failures behind a generic INTERNAL (REQ-PLT-060).
func errorStatus(cause error) *statuspb.Status {
	if st, ok := status.FromError(cause); ok && cause != nil {
		switch st.Code() {
		case codes.OK, codes.Unknown, codes.Internal, codes.DataLoss:
		default:
			return st.Proto()
		}
	}
	return status.Convert(apierr.Internal()).Proto()
}

// holdsString reports whether a top-level string field of m equals v.
func holdsString(m protoreflect.Message, v string) bool {
	found := false
	m.Range(func(fd protoreflect.FieldDescriptor, val protoreflect.Value) bool {
		if fd.Kind() == protoreflect.StringKind && !fd.IsList() && !fd.IsMap() && val.String() == v {
			found = true
		}
		return !found
	})
	return found
}
