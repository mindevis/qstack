//go:build integration

package api_test

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	apipb "github.com/mindevis/qstack/gen/go/qstack/api"
	rminternalv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/internalv1"
	rmv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/v1"
	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/db"
	"github.com/mindevis/qstack/pkg/ids"
	"github.com/mindevis/qstack/pkg/quota"
)

const instances = "compute.instances.count"

// newProject returns a project of a new organization and its owner.
func (e *env) newProject(t *testing.T) (owner string, prj *rmv1.Project) {
	t.Helper()
	owner = newUser()
	org := e.createOrg(t, "o"+ids.New(ids.Organization)[4:20], owner)
	op, err := e.projects.CreateProject(as(t.Context(), owner), &rmv1.CreateProjectRequest{OrganizationId: org.GetId(), Name: "web"})
	return owner, done(t, op, err, &rmv1.Project{})
}

func (e *env) reserve(ctx context.Context, project, op string, deltas ...quota.Delta) error {
	req := &rminternalv1.ReserveRequest{ProjectId: project, OperationId: op}
	for _, d := range deltas {
		req.Deltas = append(req.Deltas, &rminternalv1.QuotaDelta{Metric: d.Metric, Amount: d.Amount})
	}
	_, err := e.quotas.Reserve(ctx, req)
	return err
}

func (e *env) commit(t *testing.T, op string) {
	t.Helper()
	if _, err := e.quotas.Commit(t.Context(), &rminternalv1.CommitRequest{OperationId: op}); err != nil {
		t.Fatalf("commit %s: %v", op, err)
	}
}

func newOp() string { return ids.New(ids.Operation) }

func (e *env) quotaOf(t *testing.T, owner, project, metric string) *rmv1.Quota {
	t.Helper()
	resp, err := e.projects.GetQuotas(as(t.Context(), owner), &rmv1.GetQuotasRequest{ProjectId: project})
	if err != nil {
		t.Fatalf("GetQuotas: %v", err)
	}
	for _, q := range resp.GetQuotas() {
		if q.GetMetric() == metric {
			return q
		}
	}
	t.Fatalf("GetQuotas has no %s: %v", metric, resp.GetQuotas())
	return nil
}

// AC 03 §9.6 on QuotaService: with a limit of 1 the second instance is
// RESOURCE_EXHAUSTED / QS_QUOTA_EXCEEDED; after deleting the first it fits.
func TestQuotaLimit(t *testing.T) {
	e := newEnv(t)
	owner, prj := e.newProject(t)
	ctx := t.Context()
	if err := e.app.SetLimit(ctx, prj.GetId(), instances, 1); err != nil {
		t.Fatal(err)
	}
	first := newOp()
	if err := e.reserve(ctx, prj.GetId(), first, quota.Delta{Metric: instances, Amount: 1}); err != nil {
		t.Fatalf("first reserve: %v", err)
	}
	e.commit(t, first)

	err := e.reserve(ctx, prj.GetId(), newOp(), quota.Delta{Metric: instances, Amount: 1})
	if status.Code(err) != codes.ResourceExhausted || !apierr.IsReason(err, apipb.ErrorReason_QS_QUOTA_EXCEEDED) {
		t.Fatalf("second reserve: %v, want RESOURCE_EXHAUSTED / QS_QUOTA_EXCEEDED", err)
	}
	var qf *errdetails.QuotaFailure
	for _, d := range status.Convert(err).Details() {
		if v, ok := d.(*errdetails.QuotaFailure); ok {
			qf = v
		}
	}
	if qf == nil || len(qf.GetViolations()) != 1 || qf.GetViolations()[0].GetSubject() == "" {
		t.Errorf("QuotaFailure = %v", qf)
	}

	// Deleting the instance reserves and commits -1, which never fails.
	del := newOp()
	if err := e.reserve(ctx, prj.GetId(), del, quota.Delta{Metric: instances, Amount: -1}); err != nil {
		t.Fatalf("reserve deletion: %v", err)
	}
	e.commit(t, del)
	if err := e.reserve(ctx, prj.GetId(), newOp(), quota.Delta{Metric: instances, Amount: 1}); err != nil {
		t.Errorf("reserve after deletion: %v", err)
	}
	if q := e.quotaOf(t, owner, prj.GetId(), instances); q.GetLimit() != 1 || q.GetUsed() != 0 || q.GetReserved() != 1 {
		t.Errorf("quota = %v, want limit 1, used 0, reserved 1", q)
	}
}

// AC 03 §9.7 on QuotaService: 100 concurrent reservations under a limit
// of 10 reserve exactly 10 (REQ-RM-001).
func TestQuotaConcurrentReserve(t *testing.T) {
	e := newEnv(t)
	_, prj := e.newProject(t)
	if err := e.app.SetLimit(t.Context(), prj.GetId(), instances, 10); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, exhausted := 0, 0
	for range 100 {
		wg.Go(func() {
			err := e.reserve(t.Context(), prj.GetId(), newOp(), quota.Delta{Metric: instances, Amount: 1}, quota.Delta{Metric: "compute.vcpus.total", Amount: 2})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case status.Code(err) == codes.ResourceExhausted:
				exhausted++
			default:
				t.Errorf("reserve: %v", err)
			}
		})
	}
	wg.Wait()
	if ok != 10 || exhausted != 90 {
		t.Errorf("reserved %d, exhausted %d; want 10 and 90", ok, exhausted)
	}
}

// Spec 03 §5: the reservation protocol.
func TestReservationProtocol(t *testing.T) {
	e := newEnv(t)
	owner, prj := e.newProject(t)
	ctx := t.Context()
	one := quota.Delta{Metric: instances, Amount: 1}

	op := newOp()
	if err := e.reserve(ctx, prj.GetId(), op, one); err != nil {
		t.Fatal(err)
	}
	if err := e.reserve(ctx, prj.GetId(), op, one); err != nil {
		t.Errorf("repeat with the same deltas: %v", err)
	}
	if err := e.reserve(ctx, prj.GetId(), op, quota.Delta{Metric: instances, Amount: 2}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("repeat with other deltas: %v, want INVALID_ARGUMENT", err)
	}
	if q := e.quotaOf(t, owner, prj.GetId(), instances); q.GetReserved() != 1 {
		t.Errorf("reserved = %d, want 1", q.GetReserved())
	}
	e.commit(t, op)
	e.commit(t, op) // idempotent
	if q := e.quotaOf(t, owner, prj.GetId(), instances); q.GetUsed() != 1 || q.GetReserved() != 0 {
		t.Errorf("after commit: %v, want used 1", q)
	}
	if _, err := e.quotas.Release(ctx, &rminternalv1.ReleaseRequest{OperationId: op}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("release after commit: %v, want FAILED_PRECONDITION", err)
	}

	released := newOp()
	if err := e.reserve(ctx, prj.GetId(), released, one); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := e.quotas.Release(ctx, &rminternalv1.ReleaseRequest{OperationId: released}); err != nil {
			t.Errorf("release: %v", err)
		}
	}
	if _, err := e.quotas.Commit(ctx, &rminternalv1.CommitRequest{OperationId: released}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("commit after release: %v, want FAILED_PRECONDITION", err)
	}
	if _, err := e.quotas.Commit(ctx, &rminternalv1.CommitRequest{OperationId: newOp()}); status.Code(err) != codes.NotFound {
		t.Errorf("commit unknown: %v, want NOT_FOUND", err)
	}
	if _, err := e.quotas.Release(ctx, &rminternalv1.ReleaseRequest{OperationId: newOp()}); err != nil {
		t.Errorf("release unknown: %v, want success", err)
	}

	// An expired reservation stops holding quota but still commits.
	expired := newOp()
	if err := e.reserve(ctx, prj.GetId(), expired, one); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE quota_reservations SET expire_at = now() - interval '1 minute' WHERE operation_id = $1`, expired); err != nil {
		t.Fatal(err)
	}
	if q := e.quotaOf(t, owner, prj.GetId(), instances); q.GetReserved() != 0 {
		t.Errorf("expired reservation still holds %d", q.GetReserved())
	}
	e.commit(t, expired)
	if q := e.quotaOf(t, owner, prj.GetId(), instances); q.GetUsed() != 2 {
		t.Errorf("used = %d after committing an expired reservation, want 2", q.GetUsed())
	}

	// Metrics without a limit are not restricted.
	if err := e.reserve(ctx, prj.GetId(), newOp(), quota.Delta{Metric: "compute.gpus.count", Amount: 1 << 40}); err != nil {
		t.Errorf("unlimited metric: %v", err)
	}
	if err := e.reserve(ctx, prj.GetId(), newOp(), quota.Delta{Metric: "Bad", Amount: 1}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("invalid metric: %v, want INVALID_ARGUMENT", err)
	}
	if err := e.reserve(ctx, ids.New(ids.Project), newOp(), one); status.Code(err) != codes.NotFound {
		t.Errorf("unknown project: %v, want NOT_FOUND", err)
	}
}

// Spec 03 §2: a project holding resources is not deleted.
func TestDeleteProjectInUse(t *testing.T) {
	e := newEnv(t)
	owner, prj := e.newProject(t)
	ctx := t.Context()
	op := newOp()
	if err := e.reserve(ctx, prj.GetId(), op, quota.Delta{Metric: instances, Amount: 1}); err != nil {
		t.Fatal(err)
	}
	del := func() error {
		_, err := e.projects.DeleteProject(as(ctx, owner), &rmv1.DeleteProjectRequest{ProjectId: prj.GetId()})
		return err
	}
	if err := del(); status.Code(err) != codes.FailedPrecondition || !apierr.IsReason(err, apipb.ErrorReason_QS_RESOURCE_IN_USE) {
		t.Errorf("delete with a reservation: %v, want FAILED_PRECONDITION / QS_RESOURCE_IN_USE", err)
	}
	e.commit(t, op)
	if err := del(); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("delete with usage: %v, want FAILED_PRECONDITION", err)
	}
	free := newOp()
	if err := e.reserve(ctx, prj.GetId(), free, quota.Delta{Metric: instances, Amount: -1}); err != nil {
		t.Fatal(err)
	}
	e.commit(t, free)
	if err := del(); err != nil {
		t.Fatalf("delete empty project: %v", err)
	}
	if err := e.reserve(ctx, prj.GetId(), newOp(), quota.Delta{Metric: instances, Amount: 1}); status.Code(err) != codes.NotFound {
		t.Errorf("reserve in a deleted project: %v, want NOT_FOUND", err)
	}
}

// REQ-RM-002: a snapshot corrects usage to the counted values, except rows
// changed after it was counted and metrics of other services.
func TestUsageSnapshot(t *testing.T) {
	e := newEnv(t)
	_, a := e.newProject(t)
	_, b := e.newProject(t)
	_, c := e.newProject(t)
	ctx := t.Context()
	set := func(p *rmv1.Project, d ...quota.Delta) {
		op := newOp()
		if err := e.reserve(ctx, p.GetId(), op, d...); err != nil {
			t.Fatal(err)
		}
		e.commit(t, op)
	}
	set(a, quota.Delta{Metric: instances, Amount: 3}, quota.Delta{Metric: "vpc.networks.count", Amount: 2})
	set(b, quota.Delta{Metric: instances, Amount: 2})
	counted := clock.Real().Now()
	set(c, quota.Delta{Metric: instances, Amount: 1}) // after the snapshot was counted

	snap := &rminternalv1.UsageSnapshot{
		Service:      "compute",
		SnapshotTime: timestamppb.New(counted),
		Projects: []*rminternalv1.ProjectUsage{
			// a: 2 instead of 3; the vpc metric is not compute's.
			{ProjectId: a.GetId(), Usage: []*rminternalv1.QuotaDelta{{Metric: instances, Amount: 2}, {Metric: "vpc.networks.count", Amount: 0}}},
			// c changed after counting: left alone.
			{ProjectId: c.GetId(), Usage: []*rminternalv1.QuotaDelta{{Metric: instances, Amount: 0}}},
			// unknown projects are ignored.
			{ProjectId: ids.New(ids.Project), Usage: []*rminternalv1.QuotaDelta{{Metric: instances, Amount: 5}}},
		},
		// b is missing: it holds no instances.
	}
	var corrected map[string]int
	if err := db.InTx(ctx, e.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var err error
		corrected, err = e.app.ApplySnapshot(ctx, tx, snap)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if corrected[instances] != 2 || len(corrected) != 1 {
		t.Errorf("corrected = %v, want %s: 2 (a and b)", corrected, instances)
	}
	for _, tc := range []struct {
		p      *rmv1.Project
		metric string
		want   int64
	}{{a, instances, 2}, {a, "vpc.networks.count", 2}, {b, instances, 0}, {c, instances, 1}} {
		var used int64
		if err := e.pool.QueryRow(ctx, `SELECT used FROM quota_usage WHERE project_id = $1 AND metric = $2`, tc.p.GetId(), tc.metric).Scan(&used); err != nil {
			t.Fatal(err)
		}
		if used != tc.want {
			t.Errorf("%s %s used = %d, want %d", tc.p.GetName(), tc.metric, used, tc.want)
		}
	}
}
