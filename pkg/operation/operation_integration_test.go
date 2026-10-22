//go:build integration

package operation_test

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"

	apipb "github.com/mindevis/qstack/gen/go/qstack/api"
	operationv1 "github.com/mindevis/qstack/gen/go/qstack/operation/v1"
	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/db"
	"github.com/mindevis/qstack/pkg/idempotency"
	"github.com/mindevis/qstack/pkg/ids"
	"github.com/mindevis/qstack/pkg/operation"
	"github.com/mindevis/qstack/pkg/testenv"
)

var t0 = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

type env struct {
	pool  *pgxpool.Pool
	clock *clock.Fake
	ids   *ids.Generator
	ops   *operation.Store
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, db.Config{DSN: testenv.Postgres(t), ApplicationName: "qs-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := operation.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(t0)
	gen := ids.NewGenerator(clk, rand.Reader)
	return &env{pool: pool, clock: clk, ids: gen, ops: operation.New(pool, clk, gen)}
}

func (e *env) spec(resourceID string) operation.Spec {
	return operation.Spec{
		ProjectID:   "prj-01m5hqpzg00000000000000000",
		ResourceID:  resourceID,
		Description: "Create instance",
		CreatedBy:   "usr-01m5hqpzg00000000000000000",
		Metadata:    wrapperspb.String(resourceID),
	}
}

func (e *env) create(ctx context.Context, sp operation.Spec) (*operationv1.Operation, error) {
	var op *operationv1.Operation
	err := db.InTx(ctx, e.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var err error
		op, err = e.ops.Create(ctx, tx, sp)
		return err
	})
	return op, err
}

func (e *env) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMigrateIsIdempotent(t *testing.T) {
	e := newEnv(t)
	again, err := operation.Migrate(context.Background(), e.pool)
	if err != nil || len(again) != 0 {
		t.Errorf("second Migrate applied %v (err %v)", again, err)
	}
}

// REQ-PLT-040, REQ-PLT-041, REQ-PLT-042: the operation is stored in the
// service database, returned running with the resource ID in metadata, and
// completed later with the final resource.
func TestCreateGetComplete(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	res := e.ids.New(ids.Instance)

	op, err := e.create(ctx, e.spec(res))
	if err != nil {
		t.Fatal(err)
	}
	if ids.ParseKind(op.GetId(), ids.Operation) != nil || op.GetDone() || op.GetResourceId() != res {
		t.Fatalf("created %v", op)
	}
	meta := &wrapperspb.StringValue{}
	if err := op.GetMetadata().UnmarshalTo(meta); err != nil || meta.GetValue() != res {
		t.Errorf("metadata = %v (%v), want resource id", meta, err)
	}
	got, err := e.ops.Get(ctx, op.GetId())
	if err != nil || !proto.Equal(got, op) {
		t.Errorf("Get = %v, %v; want %v", got, err, op)
	}

	e.clock.Advance(3 * time.Second)
	done, err := e.ops.Complete(ctx, e.pool, op.GetId(), wrapperspb.String("final "+res))
	if err != nil {
		t.Fatal(err)
	}
	final := &wrapperspb.StringValue{}
	if !done.GetDone() || done.GetResponse().UnmarshalTo(final) != nil || final.GetValue() != "final "+res ||
		!done.GetModifyTime().AsTime().Equal(t0.Add(3*time.Second)) {
		t.Errorf("completed %v", done)
	}
	if _, err := e.ops.Complete(ctx, e.pool, op.GetId(), nil); !errors.Is(err, operation.ErrNotRunning) {
		t.Errorf("second Complete err = %v, want ErrNotRunning", err)
	}
	if _, err := e.ops.Get(ctx, "op-01m5hqpzg00000000000000000"); status.Code(err) != codes.NotFound {
		t.Errorf("Get(missing) err = %v, want NOT_FOUND", err)
	}
}

func TestFailStoresSanitizedStatus(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for cause, want := range map[error]codes.Code{
		apierr.ZoneCapacity("ru-central1-a"):         codes.ResourceExhausted,
		errors.New("ovn: nb db timeout at 10.0.0.3"): codes.Internal,
	} {
		op, err := e.create(ctx, e.spec(e.ids.New(ids.Instance)))
		if err != nil {
			t.Fatal(err)
		}
		failed, err := e.ops.Fail(ctx, e.pool, op.GetId(), cause)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := e.ops.Get(ctx, op.GetId())
		if !got.GetDone() || codes.Code(got.GetError().GetCode()) != want || !proto.Equal(got, failed) {
			t.Errorf("cause %v: stored %v, want code %v", cause, got, want)
		}
	}
}

// REQ-PLT-043: a second mutating operation on a busy resource fails with
// FAILED_PRECONDITION / QS_RESOURCE_BUSY naming the running operation.
func TestOneRunningOperationPerResource(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	res := e.ids.New(ids.Disk)

	first, err := e.create(ctx, e.spec(res))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.create(ctx, e.spec(res))
	if status.Code(err) != codes.FailedPrecondition || !apierr.IsReason(err, apipb.ErrorReason_QS_RESOURCE_BUSY) {
		t.Fatalf("err = %v, want QS_RESOURCE_BUSY", err)
	}
	if st, _ := status.FromError(err); !strings.Contains(st.String(), first.GetId()) {
		t.Errorf("busy error %v does not name %s", err, first.GetId())
	}
	if _, err := e.ops.Complete(ctx, e.pool, first.GetId(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.create(ctx, e.spec(res)); err != nil {
		t.Errorf("create after completion: %v", err)
	}
}

// REQ-PLT-043 under concurrency: exactly one of several racing transactions wins.
func TestConcurrentOperationsOnResource(t *testing.T) {
	e := newEnv(t)
	res := e.ids.New(ids.Disk)
	const n = 8
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			_, err := e.create(context.Background(), e.spec(res))
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	ok, busy := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case apierr.IsReason(err, apipb.ErrorReason_QS_RESOURCE_BUSY):
			busy++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || busy != n-1 {
		t.Errorf("ok = %d, busy = %d; want 1 and %d", ok, busy, n-1)
	}
}

func TestCancelRequest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	op, err := e.create(ctx, e.spec(e.ids.New(ids.Instance)))
	if err != nil {
		t.Fatal(err)
	}
	if requested, _ := e.ops.CancelRequested(ctx, op.GetId()); requested {
		t.Error("cancel requested before RequestCancel")
	}
	if _, err := e.ops.RequestCancel(ctx, e.pool, op.GetId()); err != nil {
		t.Fatal(err)
	}
	if requested, err := e.ops.CancelRequested(ctx, op.GetId()); !requested || err != nil {
		t.Errorf("CancelRequested = %v, %v", requested, err)
	}
	if _, err := e.ops.Fail(ctx, e.pool, op.GetId(), status.Error(codes.Canceled, "operation cancelled")); err != nil {
		t.Fatal(err)
	}
	got, err := e.ops.RequestCancel(ctx, e.pool, op.GetId())
	if err != nil || codes.Code(got.GetError().GetCode()) != codes.Canceled {
		t.Errorf("RequestCancel on finished op = %v, %v", got, err)
	}
	if _, err := e.ops.RequestCancel(ctx, e.pool, "op-01m5hqpzg00000000000000000"); status.Code(err) != codes.NotFound {
		t.Errorf("RequestCancel(missing) err = %v", err)
	}
}

// Spec 02 §5.3: finished operations are kept 30 days; keys 24 hours.
func TestPurge(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	done, _ := e.create(ctx, e.spec(e.ids.New(ids.Instance)))
	running, _ := e.create(ctx, e.spec(e.ids.New(ids.Instance)))
	if _, err := e.ops.Complete(ctx, e.pool, done.GetId(), nil); err != nil {
		t.Fatal(err)
	}

	e.clock.Advance(operation.Retention - time.Second)
	if n, err := e.ops.Purge(ctx, 100); n != 0 || err != nil {
		t.Errorf("purge before retention: %d, %v", n, err)
	}
	e.clock.Advance(time.Second)
	if n, err := e.ops.Purge(ctx, 100); n != 1 || err != nil {
		t.Errorf("purge after retention: %d, %v", n, err)
	}
	if _, err := e.ops.Get(ctx, done.GetId()); status.Code(err) != codes.NotFound {
		t.Errorf("purged operation still readable: %v", err)
	}
	if _, err := e.ops.Get(ctx, running.GetId()); err != nil {
		t.Errorf("running operation purged: %v", err)
	}
}

// --- idempotency (REQ-PLT-050) through the interceptor and a real handler ---

const createMethod = "/qstack.operation.v1.OperationService/CancelOperation" // any mutating method

// server wires the interceptor to a handler that, like a real Create
// handler, generates a new resource ID and creates the operation first in
// its transaction.
type server struct {
	e    *env
	icpt grpc.UnaryServerInterceptor
}

func newServer(e *env) *server {
	return &server{e: e, icpt: idempotency.UnaryServerInterceptor(idempotency.Options{
		Store:   e.ops,
		Subject: func(context.Context) string { return "usr-01m5hqpzg00000000000000000" },
	})}
}

func (s *server) handle(ctx context.Context, _ any) (any, error) {
	var op *operationv1.Operation
	err := db.InTx(ctx, s.e.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var err error
		op, err = s.e.ops.Create(ctx, tx, s.e.spec(s.e.ids.New(ids.Instance)))
		if err != nil {
			return err
		}
		// The resource change would follow here; hold the transaction open
		// to widen the race window.
		time.Sleep(20 * time.Millisecond)
		return nil
	})
	return op, err
}

func (s *server) call(key string, body string) (*operationv1.Operation, error) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(idempotency.MetadataKey, key))
	resp, err := s.icpt(ctx, &operationv1.CancelOperationRequest{OperationId: body}, &grpc.UnaryServerInfo{FullMethod: createMethod}, s.handle)
	if err != nil {
		return nil, err
	}
	return resp.(*operationv1.Operation), nil
}

// REQ-PLT-050: concurrent retries with one key create one operation and all
// receive it.
func TestIdempotentConcurrentRetries(t *testing.T) {
	e := newEnv(t)
	s := newServer(e)
	const n = 8
	got := make(chan string, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			op, err := s.call("2d9f7c6e-6a9b-4b7e-9c1e-5b0f3a1d2e44", "body")
			if err != nil {
				t.Errorf("call: %v", err)
				return
			}
			got <- op.GetId()
		})
	}
	wg.Wait()
	close(got)
	first := ""
	for id := range got {
		if first == "" {
			first = id
		}
		if id != first {
			t.Errorf("got operations %s and %s, want one", first, id)
		}
	}
	if c := e.count(t, "operations"); c != 1 {
		t.Errorf("%d operations stored, want 1", c)
	}
}

// REQ-PLT-050: different body → QS_IDEMPOTENCY_KEY_REUSED; after 24 h the key
// may be reused.
func TestIdempotencyKeyReuseAndExpiry(t *testing.T) {
	e := newEnv(t)
	s := newServer(e)
	first, err := s.call("k-1", "body")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.call("k-1", "other body"); !apierr.IsReason(err, apipb.ErrorReason_QS_IDEMPOTENCY_KEY_REUSED) {
		t.Errorf("different body err = %v, want QS_IDEMPOTENCY_KEY_REUSED", err)
	}
	e.clock.Advance(idempotency.TTL - time.Second)
	if again, err := s.call("k-1", "body"); err != nil || again.GetId() != first.GetId() {
		t.Errorf("repeat within TTL = %v, %v; want %s", again, err, first.GetId())
	}
	e.clock.Advance(time.Second)
	fresh, err := s.call("k-1", "other body")
	if err != nil || fresh.GetId() == first.GetId() {
		t.Errorf("after TTL = %v, %v; want a new operation", fresh, err)
	}
	if c := e.count(t, "idempotency_keys"); c != 1 {
		t.Errorf("%d keys stored, want 1 (expired key taken over)", c)
	}
}

// A request rolled back for another reason does not consume its key.
func TestRolledBackRequestReleasesKey(t *testing.T) {
	e := newEnv(t)
	ctx := idempotency.WithReservation(context.Background(), idempotency.Reservation{
		Subject: "usr-1", Key: "k", RequestHash: []byte("h"),
	})
	boom := errors.New("resource insert failed")
	err := db.InTx(ctx, e.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if _, err := e.ops.Create(ctx, tx, e.spec(e.ids.New(ids.Instance))); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	if _, err := e.create(ctx, e.spec(e.ids.New(ids.Instance))); err != nil {
		t.Errorf("key still held after rollback: %v", err)
	}
	if _, found, err := e.ops.Lookup(ctx, "usr-1", "k"); !found || err != nil {
		t.Errorf("Lookup = %v, %v", found, err)
	}
}
