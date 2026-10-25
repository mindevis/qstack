package domain

import (
	"errors"
	"maps"
	"slices"
	"time"
)

// GiB and TiB for the defaults below.
const (
	gib = int64(1) << 30
	tib = int64(1) << 40
)

// DefaultQuotas are the platform limits of every project (spec 03 §5);
// the configuration may change them, operators override them per project.
var DefaultQuotas = map[string]int64{
	"compute.instances.count":       32,
	"compute.vcpus.total":           64,
	"compute.memory.bytes":          256 * gib,
	"blockstore.disks.count":        64,
	"blockstore.ssd.bytes":          2 * tib,
	"blockstore.hdd.bytes":          4 * tib,
	"blockstore.snapshots.count":    64,
	"vpc.networks.count":            4,
	"vpc.subnets.count":             16,
	"vpc.addresses.external.count":  8,
	"vpc.securityGroups.count":      32,
	"lb.networkLoadBalancers.count": 8,
	"objectstore.buckets.count":     25,
}

// ReservationTTL is how long an unconfirmed reservation holds quota.
const ReservationTTL = time.Hour

// ReservationState is the state of a quota reservation.
type ReservationState string

// Reservation states.
const (
	Reserved  ReservationState = "RESERVED"
	Committed ReservationState = "COMMITTED"
	Released  ReservationState = "RELEASED"
)

// Reservation holds quota for an operation until it commits or releases.
type Reservation struct {
	OperationID string
	ProjectID   string
	State       ReservationState
	// Deltas are amounts per metric; negative ones free usage on commit.
	Deltas   map[string]int64
	ExpireAt time.Time
}

// ErrReservationMismatch means the operation already reserved other deltas.
var ErrReservationMismatch = errors.New("the operation already has a different reservation")

// NewReservation sums deltas per metric.
func NewReservation(op, project string, deltas map[string]int64, now time.Time) Reservation {
	return Reservation{OperationID: op, ProjectID: project, State: Reserved, Deltas: maps.Clone(deltas), ExpireAt: now.Add(ReservationTTL)}
}

// Matches reports whether a repeated Reserve asks for the same thing.
func (r *Reservation) Matches(project string, deltas map[string]int64) bool {
	return r.ProjectID == project && maps.Equal(r.Deltas, deltas)
}

// Commit moves the reservation to COMMITTED; apply reports whether usage
// must change now (false for a repeat). An expired reservation still
// commits: the resource exists (spec 03 §5).
func (r *Reservation) Commit() (apply bool, err error) {
	switch r.State {
	case Committed:
		return false, nil
	case Released:
		return false, &InvalidStateError{r.OperationID, string(r.State), "commit"}
	}
	r.State = Committed
	return true, nil
}

// Release moves the reservation to RELEASED; changed is false for a repeat.
func (r *Reservation) Release() (changed bool, err error) {
	switch r.State {
	case Released:
		return false, nil
	case Committed:
		return false, &InvalidStateError{r.OperationID, string(r.State), "release"}
	}
	r.State = Released
	return true, nil
}

// MetricUsage is the quota state of one metric of a project.
type MetricUsage struct {
	Limit int64
	// Limited is false for a metric without a limit: it is not restricted.
	Limited bool
	Used    int64
	// Reserved is held by unexpired reservations.
	Reserved int64
}

// QuotaViolation is one exceeded metric.
type QuotaViolation struct {
	Metric                 string
	Limit, Used, Requested int64
}

// CheckReserve returns the metrics whose limit deltas would exceed:
// used + reserved + delta ≤ limit for positive deltas; negative deltas and
// metrics without a limit never fail (REQ-RM-001).
func CheckReserve(deltas map[string]int64, usage map[string]MetricUsage) []QuotaViolation {
	var out []QuotaViolation
	for _, m := range slices.Sorted(maps.Keys(deltas)) {
		d := deltas[m]
		u := usage[m]
		if d <= 0 || !u.Limited {
			continue
		}
		if held := u.Used + u.Reserved; held+d > u.Limit {
			out = append(out, QuotaViolation{Metric: m, Limit: u.Limit, Used: held, Requested: d})
		}
	}
	return out
}
