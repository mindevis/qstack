// Package controller runs the background work of Resource Manager: the
// outbox relay (REQ-PLT-090), the reconciliation of quota usage with the
// services' usage snapshots (REQ-RM-002) and the purge of finished
// operations, reservations and processed event IDs. Resource changes
// complete synchronously, so there are no reconcilers.
package controller

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	rminternalv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/internalv1"
	"github.com/mindevis/qstack/internal/resourcemanager/app"
	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/events"
	"github.com/mindevis/qstack/pkg/operation"
	"github.com/mindevis/qstack/pkg/outbox"
)

// SnapshotConsumer is the durable consumer of usage snapshots.
const SnapshotConsumer = "resourcemanager-usage-snapshot"

// ReservationRetention keeps finished and expired reservations, so that a
// late Commit of an expired one still applies (spec 03 §5).
const ReservationRetention = 7 * 24 * time.Hour

// Config configures Run.
type Config struct {
	Relay      *outbox.Relay
	Operations *operation.Store
	App        *app.App
	Pool       *pgxpool.Pool
	// Consumers are the usage-snapshot consumers, one per region.
	Consumers []*events.Consumer
	Clock     clock.Clock
	Logger    *slog.Logger
	// PurgeInterval is the pause between purges; default 10 minutes.
	PurgeInterval time.Duration
}

const purgeBatch = 1000

// Run works until ctx is cancelled or a loop fails.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Relay == nil || cfg.Operations == nil || cfg.App == nil || cfg.Pool == nil || cfg.Clock == nil || cfg.Logger == nil {
		return errors.New("controller: relay, operations, app, pool, clock and logger are required")
	}
	if cfg.PurgeInterval <= 0 {
		cfg.PurgeInterval = 10 * time.Minute
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	loops := []func(context.Context) error{cfg.Relay.Run, func(ctx context.Context) error { return purge(ctx, cfg) }}
	for _, c := range cfg.Consumers {
		loops = append(loops, c.Run)
	}
	errc := make(chan error, len(loops))
	for _, l := range loops {
		go func() { errc <- l(ctx) }()
	}
	err := <-errc
	cancel()
	for range len(loops) - 1 {
		if other := <-errc; err == nil {
			err = other
		}
	}
	return err
}

func purge(ctx context.Context, cfg Config) error {
	t := time.NewTicker(cfg.PurgeInterval)
	defer t.Stop()
	for {
		for {
			n, err := cfg.Operations.Purge(ctx, purgeBatch)
			if err != nil {
				cfg.warn(ctx, "purge operations", err)
				break
			}
			if n < purgeBatch {
				break
			}
		}
		if _, err := cfg.App.PurgeReservations(ctx, cfg.Clock.Now().Add(-ReservationRetention)); err != nil {
			cfg.warn(ctx, "purge reservations", err)
		}
		if _, err := events.PurgeProcessed(ctx, cfg.Pool, cfg.Clock); err != nil {
			cfg.warn(ctx, "purge processed events", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func (cfg Config) warn(ctx context.Context, what string, err error) {
	if ctx.Err() == nil {
		cfg.Logger.WarnContext(ctx, what, "error", err)
	}
}

// SnapshotHandler applies usage snapshots (REQ-RM-002); corrections are
// counted in metrics (nil: not counted).
func SnapshotHandler(a *app.App, m *Metrics) events.Handler {
	return func(ctx context.Context, tx pgx.Tx, ev events.Event) error {
		snap := &rminternalv1.UsageSnapshot{}
		if err := ev.DecodeData(snap); err != nil {
			return err
		}
		corrected, err := a.ApplySnapshot(ctx, tx, snap)
		if err != nil {
			return err
		}
		if m != nil {
			for metric, n := range corrected {
				m.drift.WithLabelValues(metric).Add(float64(n))
			}
		}
		return nil
	}
}

// Metrics are the quota reconciliation metrics.
type Metrics struct{ drift *prometheus.CounterVec }

// NewMetrics registers qs_quota_usage_drift_total{metric}: usage rows the
// snapshots corrected, i.e. a service and Resource Manager disagreed.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{drift: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "qs_quota_usage_drift_total", Help: "Quota usage rows corrected by usage snapshots.",
	}, []string{"metric"})}
	reg.MustRegister(m.drift)
	return m
}
