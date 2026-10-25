package app

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	rminternalv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/internalv1"
	"github.com/mindevis/qstack/internal/resourcemanager/domain"
	"github.com/mindevis/qstack/internal/resourcemanager/store"
	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/db"
)

// limits returns the effective limits of a project: the defaults with the
// project's overrides.
func (a *App) limits(ctx context.Context, s *store.Store, project string) (map[string]int64, error) {
	over, err := s.Limits(ctx, project)
	if err != nil {
		return nil, err
	}
	out := maps.Clone(a.cfg.QuotaDefaults)
	if out == nil {
		out = map[string]int64{}
	}
	maps.Copy(out, over)
	return out, nil
}

// Reserve holds deltas of a project for an operation (REQ-RM-001): one
// transaction locks the project's usage rows of the metrics and checks
// used + active reservations + delta ≤ limit. A repeat with the same
// deltas returns the existing reservation's expiry.
func (a *App) Reserve(ctx context.Context, project, op string, deltas map[string]int64) (time.Time, error) {
	for attempt := 0; ; attempt++ {
		var expire time.Time
		err := db.InTx(ctx, a.cfg.Pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
			var err error
			expire, err = a.reserve(ctx, store.New(tx), project, op, deltas)
			return err
		})
		if errors.Is(err, store.ErrReservationExists) && attempt == 0 {
			continue // a concurrent repeat won: compare with its reservation
		}
		return expire, err
	}
}

func (a *App) reserve(ctx context.Context, s *store.Store, project, op string, deltas map[string]int64) (time.Time, error) {
	status, err := s.LockLiveProject(ctx, project)
	if errors.Is(err, store.ErrNotFound) {
		return time.Time{}, apierr.NotFound("project", project)
	}
	if err != nil {
		return time.Time{}, err
	}
	existing, err := s.GetReservation(ctx, op)
	switch {
	case err == nil:
		if !existing.Matches(project, deltas) {
			return time.Time{}, apierr.InvalidFields(apierr.FieldViolation{Field: "operation_id", Description: domain.ErrReservationMismatch.Error()})
		}
		return existing.ExpireAt, nil
	case !errors.Is(err, store.ErrNotFound):
		return time.Time{}, err
	}
	growing := slices.ContainsFunc(slices.Collect(maps.Values(deltas)), func(d int64) bool { return d > 0 })
	if growing && status != domain.ProjectActive {
		return time.Time{}, apierr.InvalidState(project, string(status), "reserve quota")
	}
	now := a.now()
	used, err := s.LockUsage(ctx, project, slices.Collect(maps.Keys(deltas)), now)
	if err != nil {
		return time.Time{}, err
	}
	reserved, err := s.Reserved(ctx, project, now)
	if err != nil {
		return time.Time{}, err
	}
	limits, err := a.limits(ctx, s, project)
	if err != nil {
		return time.Time{}, err
	}
	usage := make(map[string]domain.MetricUsage, len(deltas))
	for m := range deltas {
		l, ok := limits[m]
		usage[m] = domain.MetricUsage{Limit: l, Limited: ok, Used: used[m], Reserved: reserved[m]}
	}
	if vs := domain.CheckReserve(deltas, usage); len(vs) > 0 {
		out := make([]apierr.QuotaViolation, len(vs))
		for i, v := range vs {
			out[i] = apierr.QuotaViolation{Metric: v.Metric, Limit: v.Limit, Used: v.Used, Requested: v.Requested}
		}
		return time.Time{}, apierr.QuotaExceeded(out...)
	}
	r := domain.NewReservation(op, project, deltas, now)
	return r.ExpireAt, s.InsertReservation(ctx, r, now)
}

// Commit turns a reservation into usage; idempotent, and applied even
// after the reservation expired (spec 03 §5).
func (a *App) Commit(ctx context.Context, op string) error {
	return db.InTx(ctx, a.cfg.Pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		s := store.New(tx)
		r, err := s.GetReservation(ctx, op)
		if errors.Is(err, store.ErrNotFound) {
			return apierr.NotFound("reservation", op)
		}
		if err != nil {
			return err
		}
		apply, err := r.Commit()
		if err != nil || !apply {
			return apiError(err, "reservation", op)
		}
		now := a.now()
		if _, err := s.LockUsage(ctx, r.ProjectID, slices.Collect(maps.Keys(r.Deltas)), now); err != nil {
			return err
		}
		for _, m := range slices.Sorted(maps.Keys(r.Deltas)) {
			if err := s.AddUsage(ctx, r.ProjectID, m, r.Deltas[m], now); err != nil {
				return err
			}
		}
		return s.SetReservationState(ctx, op, r.State, now)
	})
}

// Release drops a reservation; idempotent, unknown reservations included.
func (a *App) Release(ctx context.Context, op string) error {
	return db.InTx(ctx, a.cfg.Pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		s := store.New(tx)
		r, err := s.GetReservation(ctx, op)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		changed, err := r.Release()
		if err != nil || !changed {
			return apiError(err, "reservation", op)
		}
		return s.SetReservationState(ctx, op, r.State, a.now())
	})
}

// Quota is the state of one metric of a project.
type Quota struct {
	Metric                string
	Limit, Used, Reserved int64
}

// GetQuotas returns the limited metrics of a project with their usage.
func (a *App) GetQuotas(ctx context.Context, project string) ([]Quota, error) {
	s := store.New(a.cfg.Pool)
	limits, err := a.limits(ctx, s, project)
	if err != nil {
		return nil, err
	}
	used, err := s.Usage(ctx, project)
	if err != nil {
		return nil, err
	}
	reserved, err := s.Reserved(ctx, project, a.now())
	if err != nil {
		return nil, err
	}
	out := make([]Quota, 0, len(limits))
	for _, m := range slices.Sorted(maps.Keys(limits)) {
		out = append(out, Quota{Metric: m, Limit: limits[m], Used: used[m], Reserved: reserved[m]})
	}
	return out, nil
}

// SetLimit overrides a project's limit (operators; admin API later).
func (a *App) SetLimit(ctx context.Context, project, metric string, limit int64) error {
	return store.New(a.cfg.Pool).SetLimit(ctx, project, metric, limit, a.now())
}

// ApplySnapshot corrects committed usage of the snapshot's service to the
// counted values (REQ-RM-002) in tx, the transaction of the event
// consumer. Rows changed after the snapshot was counted are left alone:
// the next snapshot covers them. It returns the corrected rows per metric.
func (a *App) ApplySnapshot(ctx context.Context, tx pgx.Tx, snap *rminternalv1.UsageSnapshot) (map[string]int, error) {
	if snap.GetService() == "" || snap.GetSnapshotTime() == nil {
		return nil, errors.New("usage snapshot without service or time")
	}
	s := store.New(tx)
	prefix := snap.GetService() + "."
	var projects []string
	for _, p := range snap.GetProjects() {
		projects = append(projects, p.GetProjectId())
	}
	live, err := s.LiveProjects(ctx, projects)
	if err != nil {
		return nil, err
	}
	actual := map[store.UsageKey]int64{}
	for _, p := range snap.GetProjects() {
		if !live[p.GetProjectId()] {
			continue
		}
		for _, u := range p.GetUsage() {
			if strings.HasPrefix(u.GetMetric(), prefix) {
				actual[store.UsageKey{ProjectID: p.GetProjectId(), Metric: u.GetMetric()}] += u.GetAmount()
			}
		}
	}
	current, err := s.NonZeroUsage(ctx, prefix)
	if err != nil {
		return nil, err
	}
	for k := range current {
		if _, ok := actual[k]; !ok {
			actual[k] = 0 // not counted: no resources left
		}
	}
	t := snap.GetSnapshotTime().AsTime()
	now := a.now()
	corrected := map[string]int{}
	for k, v := range actual {
		if _, ok := current[k]; !ok && v <= 0 {
			continue
		}
		changed, err := s.CorrectUsage(ctx, k, max(v, 0), t, now)
		if err != nil {
			return nil, err
		}
		if changed {
			corrected[k.Metric]++
		}
	}
	return corrected, nil
}

// PurgeReservations deletes reservations finished or expired before before.
func (a *App) PurgeReservations(ctx context.Context, before time.Time) (int64, error) {
	return store.New(a.cfg.Pool).PurgeReservations(ctx, before)
}
