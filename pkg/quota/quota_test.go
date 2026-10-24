package quota_test

import (
	"context"
	"crypto/rand"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	apipb "github.com/mindevis/qstack/gen/go/qstack/api"
	rminternalv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/internalv1"
	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/ids"
	"github.com/mindevis/qstack/pkg/quota"
	"github.com/mindevis/qstack/pkg/quota/quotafake"
)

const (
	project   = "prj-01m5hqpzg00000000000000000"
	instances = "compute.instances.count"
	vcpus     = "compute.vcpus.total"
)

// newClient serves the fake over gRPC, so error codes and details are
// checked as a service sees them.
func newClient(t *testing.T, defaults map[string]int64) (*quota.Client, *quotafake.Service, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC))
	fake := quotafake.New(clk, defaults)
	srv := grpc.NewServer()
	rminternalv1.RegisterQuotaServiceServer(srv, fake.Server())
	lis := bufconn.Listen(1 << 20)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return quota.NewClient(conn), fake, clk
}

var gen = ids.NewGenerator(clock.Real(), rand.Reader)

func opID() string { return gen.New(ids.Operation) }

// Spec 03 §9.6: quota 1 → the second create fails with QS_QUOTA_EXCEEDED
// and QuotaFailure; after the first is deleted the next one succeeds.
func TestReserveCommitAndDelete(t *testing.T) {
	c, fake, _ := newClient(t, map[string]int64{instances: 1})
	ctx := context.Background()
	first := opID()
	if err := c.Reserve(ctx, project, first, quota.Delta{instances, 1}); err != nil {
		t.Fatal(err)
	}
	if err := c.Commit(ctx, first); err != nil {
		t.Fatal(err)
	}
	err := c.Reserve(ctx, project, opID(), quota.Delta{instances, 1})
	if status.Code(err) != codes.ResourceExhausted || !apierr.IsReason(err, apipb.ErrorReason_QS_QUOTA_EXCEEDED) {
		t.Fatalf("second create: %v", err)
	}
	var qf *errdetails.QuotaFailure
	for _, d := range status.Convert(err).Details() {
		if v, ok := d.(*errdetails.QuotaFailure); ok {
			qf = v
		}
	}
	if qf == nil || qf.GetViolations()[0].GetSubject() != instances ||
		qf.GetViolations()[0].GetDescription() != "limit 1, used 1, requested 1" {
		t.Errorf("QuotaFailure = %v", qf)
	}

	del := opID()
	if err := c.Reserve(ctx, project, del, quota.Delta{instances, -1}); err != nil {
		t.Fatalf("delete never fails on quota: %v", err)
	}
	if err := c.Commit(ctx, del); err != nil {
		t.Fatal(err)
	}
	if fake.Used(project, instances) != 0 {
		t.Errorf("used = %d after delete", fake.Used(project, instances))
	}
	if err := c.Reserve(ctx, project, opID(), quota.Delta{instances, 1}); err != nil {
		t.Errorf("create after delete: %v", err)
	}
}

// Spec 03 §9.7, REQ-RM-001: 100 parallel creates with quota 10 → exactly 10.
func TestParallelReservationsNeverExceed(t *testing.T) {
	c, fake, _ := newClient(t, map[string]int64{instances: 10, vcpus: 40})
	var ok, exhausted atomic.Int32
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			err := c.Reserve(context.Background(), project, opID(), quota.Delta{instances, 1}, quota.Delta{vcpus, 2})
			switch {
			case err == nil:
				ok.Add(1)
			case apierr.IsReason(err, apipb.ErrorReason_QS_QUOTA_EXCEEDED):
				exhausted.Add(1)
			default:
				t.Errorf("unexpected: %v", err)
			}
		})
	}
	wg.Wait()
	if ok.Load() != 10 || exhausted.Load() != 90 || fake.Reserved(project, instances) != 10 {
		t.Errorf("ok %d, exhausted %d, reserved %d; want 10, 90, 10", ok.Load(), exhausted.Load(), fake.Reserved(project, instances))
	}
}

// Reserve is idempotent per operation; Commit and Release are idempotent
// and exclusive.
func TestIdempotency(t *testing.T) {
	c, fake, _ := newClient(t, map[string]int64{instances: 1})
	ctx := context.Background()
	op := opID()
	for range 2 {
		if err := c.Reserve(ctx, project, op, quota.Delta{instances, 1}); err != nil {
			t.Fatalf("retried reserve: %v", err)
		}
	}
	if fake.Reserved(project, instances) != 1 {
		t.Error("retried reserve held quota twice")
	}
	if err := c.Reserve(ctx, project, op, quota.Delta{instances, 2}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("reserve with other deltas: %v", err)
	}
	for range 2 {
		if err := c.Commit(ctx, op); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	if fake.Used(project, instances) != 1 {
		t.Error("double commit counted twice")
	}
	if err := c.Release(ctx, op); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("release after commit: %v", err)
	}

	other := opID()
	_ = c.Reserve(ctx, project, other, quota.Delta{vcpus, 4})
	for range 2 {
		if err := c.Release(ctx, other); err != nil {
			t.Fatalf("release: %v", err)
		}
	}
	if err := c.Commit(ctx, other); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("commit after release: %v", err)
	}
	if err := c.Commit(ctx, opID()); status.Code(err) != codes.NotFound {
		t.Errorf("commit of unknown reservation: %v", err)
	}
}

// Unconfirmed reservations stop holding quota after an hour; a late commit
// still records the resource.
func TestExpiry(t *testing.T) {
	c, fake, clk := newClient(t, map[string]int64{instances: 1})
	ctx := context.Background()
	stale := opID()
	if err := c.Reserve(ctx, project, stale, quota.Delta{instances, 1}); err != nil {
		t.Fatal(err)
	}
	if err := c.Reserve(ctx, project, opID(), quota.Delta{instances, 1}); err == nil {
		t.Fatal("quota not held by the reservation")
	}
	clk.Advance(quota.ReservationTTL)
	if err := c.Reserve(ctx, project, opID(), quota.Delta{instances, 1}); err != nil {
		t.Fatalf("expired reservation still held quota: %v", err)
	}
	if err := c.Commit(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if fake.Used(project, instances) != 1 {
		t.Error("late commit not counted")
	}
}

// Metrics without a limit are unlimited; per-project limits override defaults.
func TestLimits(t *testing.T) {
	c, fake, _ := newClient(t, map[string]int64{instances: 1})
	ctx := context.Background()
	if err := c.Reserve(ctx, project, opID(), quota.Delta{"compute.gpus.total", 1 << 40}); err != nil {
		t.Errorf("unlimited metric: %v", err)
	}
	fake.SetLimit(project, instances, 3)
	for range 3 {
		if err := c.Reserve(ctx, project, opID(), quota.Delta{instances, 1}); err != nil {
			t.Fatalf("within raised limit: %v", err)
		}
	}
	if err := c.Reserve(ctx, project, opID(), quota.Delta{instances, 1}); err == nil {
		t.Error("over the raised limit")
	}
}
