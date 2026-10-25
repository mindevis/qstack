package domain_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/mindevis/qstack/internal/resourcemanager/domain"
)

// REQ-RM-001: used + reserved + delta ≤ limit for positive deltas;
// negative deltas and metrics without a limit never fail.
func TestCheckReserve(t *testing.T) {
	usage := map[string]domain.MetricUsage{
		"compute.instances.count": {Limit: 2, Limited: true, Used: 1},
		"compute.vcpus.total":     {Limit: 8, Limited: true, Used: 4, Reserved: 2},
		"compute.gpus.count":      {},
	}
	for _, tc := range []struct {
		name   string
		deltas map[string]int64
		want   []domain.QuotaViolation
	}{
		{"fits", map[string]int64{"compute.instances.count": 1, "compute.vcpus.total": 2}, nil},
		{"over", map[string]int64{"compute.instances.count": 1, "compute.vcpus.total": 3},
			[]domain.QuotaViolation{{Metric: "compute.vcpus.total", Limit: 8, Used: 6, Requested: 3}}},
		{"both over, sorted", map[string]int64{"compute.vcpus.total": 9, "compute.instances.count": 2},
			[]domain.QuotaViolation{
				{Metric: "compute.instances.count", Limit: 2, Used: 1, Requested: 2},
				{Metric: "compute.vcpus.total", Limit: 8, Used: 6, Requested: 9},
			}},
		{"negative never fails", map[string]int64{"compute.vcpus.total": -100}, nil},
		{"unlimited", map[string]int64{"compute.gpus.count": 1000}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.CheckReserve(tc.deltas, usage); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("CheckReserve = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// Spec 03 §5: Commit and Release are idempotent; Commit after Release and
// Release after Commit are FAILED_PRECONDITION.
func TestReservationStates(t *testing.T) {
	r := domain.NewReservation("op-1", "prj-1", map[string]int64{"m.a.b": 1}, t0)
	if !r.ExpireAt.Equal(t0.Add(domain.ReservationTTL)) {
		t.Errorf("expire = %v", r.ExpireAt)
	}
	if !r.Matches("prj-1", map[string]int64{"m.a.b": 1}) || r.Matches("prj-1", map[string]int64{"m.a.b": 2}) || r.Matches("prj-2", r.Deltas) {
		t.Error("Matches")
	}
	var se *domain.InvalidStateError
	if apply, err := r.Commit(); !apply || err != nil {
		t.Fatalf("Commit = %v, %v", apply, err)
	}
	if apply, err := r.Commit(); apply || err != nil {
		t.Errorf("repeat Commit = %v, %v", apply, err)
	}
	if _, err := r.Release(); !errors.As(err, &se) {
		t.Errorf("Release after Commit: %v", err)
	}

	r = domain.NewReservation("op-2", "prj-1", nil, t0)
	if changed, err := r.Release(); !changed || err != nil {
		t.Fatalf("Release = %v, %v", changed, err)
	}
	if changed, err := r.Release(); changed || err != nil {
		t.Errorf("repeat Release = %v, %v", changed, err)
	}
	if _, err := r.Commit(); !errors.As(err, &se) {
		t.Errorf("Commit after Release: %v", err)
	}
}
