// Package quotafake is an in-memory QuotaService with the semantics of
// qs-resource-manager (spec 03 §5, REQ-RM-001): reservations are checked
// atomically against limits, so concurrent callers never exceed them.
package quotafake

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	rminternalv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/internalv1"
	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/quota"
)

type state int

const (
	reserved state = iota
	committed
	released
)

type reservation struct {
	project string
	deltas  map[string]int64
	expires time.Time
	state   state
}

// Service implements quota.Quotas and the gRPC QuotaService.
type Service struct {
	rminternalv1.UnimplementedQuotaServiceServer

	mu       sync.Mutex
	clock    clock.Clock
	defaults map[string]int64            // metric → limit for every project
	limits   map[string]map[string]int64 // project → metric → limit
	used     map[string]map[string]int64
	res      map[string]*reservation // operation ID → reservation
}

// New returns a Service whose projects get defaults as limits; a metric
// without a limit is unlimited.
func New(clk clock.Clock, defaults map[string]int64) *Service {
	return &Service{clock: clk, defaults: maps.Clone(defaults),
		limits: map[string]map[string]int64{}, used: map[string]map[string]int64{}, res: map[string]*reservation{}}
}

// SetLimit overrides a project's limit.
func (s *Service) SetLimit(project, metric string, limit int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.limits[project] == nil {
		s.limits[project] = map[string]int64{}
	}
	s.limits[project][metric] = limit
}

// Used returns committed usage.
func (s *Service) Used(project, metric string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used[project][metric]
}

// Reserved returns quota held by unexpired reservations.
func (s *Service) Reserved(project, metric string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reservedLocked(project, metric)
}

func (s *Service) limit(project, metric string) (int64, bool) {
	if l, ok := s.limits[project][metric]; ok {
		return l, true
	}
	l, ok := s.defaults[metric]
	return l, ok
}

func (s *Service) reservedLocked(project, metric string) int64 {
	now := s.clock.Now()
	var n int64
	for _, r := range s.res {
		if r.project == project && r.state == reserved && now.Before(r.expires) && r.deltas[metric] > 0 {
			n += r.deltas[metric]
		}
	}
	return n
}

// Reserve implements quota.Quotas.
func (s *Service) Reserve(_ context.Context, project, op string, deltas ...quota.Delta) error {
	want := map[string]int64{}
	for _, d := range deltas {
		want[d.Metric] += d.Amount
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.res[op]; ok {
		if r.project != project || !maps.Equal(r.deltas, want) {
			return apierr.InvalidFields(apierr.FieldViolation{Field: "operation_id", Description: "already has a different reservation"})
		}
		return nil
	}
	var violations []apierr.QuotaViolation
	for _, m := range slices.Sorted(maps.Keys(want)) {
		amount := want[m]
		limit, ok := s.limit(project, m)
		if amount <= 0 || !ok {
			continue
		}
		used := s.used[project][m] + s.reservedLocked(project, m)
		if used+amount > limit {
			violations = append(violations, apierr.QuotaViolation{Metric: m, Limit: limit, Used: used, Requested: amount})
		}
	}
	if len(violations) > 0 {
		return apierr.QuotaExceeded(violations...)
	}
	s.res[op] = &reservation{project: project, deltas: want, expires: s.clock.Now().Add(quota.ReservationTTL)}
	return nil
}

// Commit implements quota.Quotas. An expired reservation is still applied:
// the resource exists, so usage must count it.
func (s *Service) Commit(_ context.Context, op string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.res[op]
	switch {
	case !ok:
		return apierr.NotFound("reservation", op)
	case r.state == committed:
		return nil
	case r.state == released:
		return apierr.InvalidState(op, "RELEASED", "commit")
	}
	if s.used[r.project] == nil {
		s.used[r.project] = map[string]int64{}
	}
	for m, d := range r.deltas {
		s.used[r.project][m] = max(s.used[r.project][m]+d, 0)
	}
	r.state = committed
	return nil
}

// Release implements quota.Quotas.
func (s *Service) Release(_ context.Context, op string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.res[op]
	switch {
	case !ok, r.state == released:
		return nil
	case r.state == committed:
		return apierr.InvalidState(op, "COMMITTED", "release")
	}
	r.state = released
	return nil
}

// Server returns the gRPC server side of the fake.
func (s *Service) Server() rminternalv1.QuotaServiceServer { return server{s} }

type server struct{ *Service }

func (s server) Reserve(ctx context.Context, req *rminternalv1.ReserveRequest) (*rminternalv1.ReserveResponse, error) {
	deltas := make([]quota.Delta, 0, len(req.GetDeltas()))
	for _, d := range req.GetDeltas() {
		deltas = append(deltas, quota.Delta{Metric: d.GetMetric(), Amount: d.GetAmount()})
	}
	if err := s.Service.Reserve(ctx, req.GetProjectId(), req.GetOperationId(), deltas...); err != nil {
		return nil, err
	}
	s.mu.Lock()
	exp := s.res[req.GetOperationId()].expires
	s.mu.Unlock()
	return &rminternalv1.ReserveResponse{ExpireTime: timestamppb.New(exp)}, nil
}

func (s server) Commit(ctx context.Context, req *rminternalv1.CommitRequest) (*rminternalv1.CommitResponse, error) {
	return &rminternalv1.CommitResponse{}, s.Service.Commit(ctx, req.GetOperationId())
}

func (s server) Release(ctx context.Context, req *rminternalv1.ReleaseRequest) (*rminternalv1.ReleaseResponse, error) {
	return &rminternalv1.ReleaseResponse{}, s.Service.Release(ctx, req.GetOperationId())
}
