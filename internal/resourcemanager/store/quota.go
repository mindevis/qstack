package store

import (
	"context"
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/mindevis/qstack/internal/resourcemanager/domain"
	"github.com/mindevis/qstack/internal/resourcemanager/store/sqlcdb"
	"github.com/mindevis/qstack/pkg/db"
)

// ErrReservationExists means a concurrent Reserve stored the operation's
// reservation first.
// The transaction is aborted; retry it.
var ErrReservationExists = errors.New("store: reservation exists")

// LockLiveProject locks a live project against deletion and returns its
// status; ErrNotFound if it does not exist or is deleted.
func (s *Store) LockLiveProject(ctx context.Context, id string) (domain.ProjectStatus, error) {
	st, err := s.q.LockLiveProject(ctx, id)
	return domain.ProjectStatus(st), notFound(err)
}

// GetReservation locks and returns a reservation.
func (s *Store) GetReservation(ctx context.Context, op string) (domain.Reservation, error) {
	r, err := s.q.GetReservationForUpdate(ctx, op)
	if err != nil {
		return domain.Reservation{}, notFound(err)
	}
	rows, err := s.q.ListReservationDeltas(ctx, op)
	if err != nil {
		return domain.Reservation{}, err
	}
	res := domain.Reservation{
		OperationID: r.OperationID, ProjectID: r.ProjectID, State: domain.ReservationState(r.State),
		ExpireAt: r.ExpireAt, Deltas: make(map[string]int64, len(rows)),
	}
	for _, d := range rows {
		res.Deltas[d.Metric] = d.Amount
	}
	return res, nil
}

// InsertReservation stores a new reservation; ErrReservationExists if the
// operation already has one.
func (s *Store) InsertReservation(ctx context.Context, r domain.Reservation, now time.Time) error {
	err := s.q.InsertReservation(ctx, sqlcdb.InsertReservationParams{
		OperationID: r.OperationID, ProjectID: r.ProjectID, State: string(r.State), ExpireAt: r.ExpireAt, CreatedAt: now,
	})
	if db.IsUniqueViolation(err, "quota_reservations_pkey") {
		return ErrReservationExists
	}
	if err != nil {
		return err
	}
	for _, m := range slices.Sorted(maps.Keys(r.Deltas)) {
		if err := s.q.InsertReservationDelta(ctx, sqlcdb.InsertReservationDeltaParams{
			OperationID: r.OperationID, ProjectID: r.ProjectID, Metric: m, Amount: r.Deltas[m],
		}); err != nil {
			return err
		}
	}
	return nil
}

// SetReservationState stores the state of a reservation.
func (s *Store) SetReservationState(ctx context.Context, op string, st domain.ReservationState, now time.Time) error {
	return s.q.SetReservationState(ctx, sqlcdb.SetReservationStateParams{OperationID: op, State: string(st), UpdatedAt: now})
}

// LockUsage locks the usage rows of metrics, creating missing ones, in
// metric order so that concurrent callers cannot deadlock (REQ-RM-001).
// It returns used per metric.
func (s *Store) LockUsage(ctx context.Context, project string, metrics []string, now time.Time) (map[string]int64, error) {
	metrics = slices.Sorted(slices.Values(metrics))
	for _, m := range metrics {
		if err := s.q.EnsureUsage(ctx, sqlcdb.EnsureUsageParams{ProjectID: project, Metric: m, UpdatedAt: now}); err != nil {
			return nil, err
		}
	}
	rows, err := s.q.LockUsage(ctx, sqlcdb.LockUsageParams{ProjectID: project, Metrics: metrics})
	if err != nil {
		return nil, err
	}
	used := make(map[string]int64, len(rows))
	for _, r := range rows {
		used[r.Metric] = r.Used
	}
	return used, nil
}

// AddUsage changes committed usage; it never goes below zero.
func (s *Store) AddUsage(ctx context.Context, project, metric string, amount int64, now time.Time) error {
	return s.q.AddUsage(ctx, sqlcdb.AddUsageParams{ProjectID: project, Metric: metric, Amount: amount, UpdatedAt: now})
}

// Reserved returns the quota held by unexpired reservations per metric.
func (s *Store) Reserved(ctx context.Context, project string, now time.Time) (map[string]int64, error) {
	rows, err := s.q.ReservedByMetric(ctx, sqlcdb.ReservedByMetricParams{ProjectID: project, Now: now})
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.Metric] = r.Reserved
	}
	return out, nil
}

// Limits returns the per-project limit overrides.
func (s *Store) Limits(ctx context.Context, project string) (map[string]int64, error) {
	rows, err := s.q.ListLimits(ctx, project)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.Metric] = r.Limit
	}
	return out, nil
}

// SetLimit overrides a project's limit of a metric.
func (s *Store) SetLimit(ctx context.Context, project, metric string, limit int64, now time.Time) error {
	return s.q.UpsertLimit(ctx, sqlcdb.UpsertLimitParams{ProjectID: project, Metric: metric, Limit: limit, UpdatedAt: now})
}

// Usage returns committed usage per metric.
func (s *Store) Usage(ctx context.Context, project string) (map[string]int64, error) {
	rows, err := s.q.ListUsage(ctx, project)
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.Metric] = r.Used
	}
	return out, nil
}

// ProjectInUse reports whether a project has usage or active reservations.
func (s *Store) ProjectInUse(ctx context.Context, project string, now time.Time) (bool, error) {
	v, err := s.q.ProjectInUse(ctx, sqlcdb.ProjectInUseParams{ProjectID: project, Now: now})
	return v.Bool, err
}

// LiveProjects returns which of ids are live projects.
func (s *Store) LiveProjects(ctx context.Context, ids []string) (map[string]bool, error) {
	rows, err := s.q.LiveProjects(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, id := range rows {
		out[id] = true
	}
	return out, nil
}

// UsageKey identifies a usage row.
type UsageKey struct{ ProjectID, Metric string }

// NonZeroUsage returns the non-zero usage of metrics starting with prefix.
func (s *Store) NonZeroUsage(ctx context.Context, prefix string) (map[UsageKey]int64, error) {
	rows, err := s.q.ServiceUsage(ctx, prefix)
	if err != nil {
		return nil, err
	}
	out := make(map[UsageKey]int64, len(rows))
	for _, r := range rows {
		out[UsageKey{r.ProjectID, r.Metric}] = r.Used
	}
	return out, nil
}

// CorrectUsage sets used to the snapshot value unless the row changed
// after snapshotTime; it reports whether the row changed.
func (s *Store) CorrectUsage(ctx context.Context, k UsageKey, used int64, snapshotTime, now time.Time) (bool, error) {
	n, err := s.q.CorrectUsage(ctx, sqlcdb.CorrectUsageParams{
		ProjectID: k.ProjectID, Metric: k.Metric, Used: used, UpdatedAt: now, SnapshotTime: snapshotTime,
	})
	return n > 0, err
}

// PurgeReservations deletes finished reservations and expired ones last
// changed before before.
func (s *Store) PurgeReservations(ctx context.Context, before time.Time) (int64, error) {
	return s.q.PurgeReservations(ctx, before)
}
