// Package reconcile runs controllers (spec 02 §9): a Reconciler drives one
// resource at a time towards its desired state, fed by a deduplicating work
// queue with exponential backoff, by events and by periodic resync.
//
// Work is split into K shards (hash(id) mod K). Replicas of a controller
// lease shards in PostgreSQL (controller_leases, TTL 15s) and share them
// evenly; a replica that dies loses its shards within the TTL plus one sync
// interval. Services never use the Kubernetes API for this.
package reconcile

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/db"
)

// Reconciler drives resource id to its desired state. It must be
// idempotent: it may run again for the same state at any time.
type Reconciler interface {
	Reconcile(ctx context.Context, id string) (Result, error)
}

// ReconcilerFunc adapts a function to Reconciler.
type ReconcilerFunc func(ctx context.Context, id string) (Result, error)

// Reconcile calls f.
func (f ReconcilerFunc) Reconcile(ctx context.Context, id string) (Result, error) { return f(ctx, id) }

// Result tells the controller when to look at the resource again.
type Result struct {
	// RequeueAfter > 0 reconciles the resource again after this delay
	// (e.g. polling a long step); 0 waits for the next event or resync.
	RequeueAfter time.Duration
}

// Lister returns the IDs of all live resources, for resync. The controller
// keeps those in its shards.
type Lister func(ctx context.Context) ([]string, error)

// VersionTable tracks this package's migrations separately from the service's.
const VersionTable = "qs_reconcile_db_version"

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate creates or upgrades the lease tables in the service's database.
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	return db.MigrateTable(ctx, pool, sub, VersionTable)
}

// Defaults (spec 02 §9).
const (
	DefaultShards         = 16
	DefaultWorkers        = 4
	DefaultLeaseTTL       = 15 * time.Second
	DefaultSyncInterval   = 3 * time.Second
	DefaultResyncInterval = 10 * time.Minute
)

// Config configures a Controller.
type Config struct {
	// Name identifies the controller across replicas, e.g. "compute-instance".
	Name       string
	Pool       *pgxpool.Pool
	Reconciler Reconciler
	Lister     Lister
	Clock      clock.Clock
	Logger     *slog.Logger
	Metrics    *Metrics // optional

	// Member identifies this replica; default hostname + random suffix.
	Member string
	// Shards (K) must be the same in all replicas; default 16.
	Shards int
	// Workers is the number of parallel reconciles; default 4.
	Workers int
	// LeaseTTL is the shard lease lifetime, default 15s; SyncInterval is
	// how often leases are renewed and rebalanced, default 3s. A dead
	// replica's shards move within LeaseTTL + SyncInterval.
	LeaseTTL     time.Duration
	SyncInterval time.Duration
	// ResyncInterval reconciles every live resource periodically; default 10m.
	ResyncInterval time.Duration
}

// Controller runs a Reconciler over the resources of its shards.
type Controller struct {
	cfg    Config
	queue  *queue
	leases *leaser
	tracer trace.Tracer
}

// New validates cfg and returns a Controller.
func New(cfg Config) (*Controller, error) {
	if cfg.Name == "" || cfg.Pool == nil || cfg.Reconciler == nil || cfg.Lister == nil || cfg.Clock == nil || cfg.Logger == nil {
		return nil, errors.New("reconcile: name, pool, reconciler, lister, clock and logger are required")
	}
	if cfg.Member == "" {
		host, _ := os.Hostname()
		cfg.Member = host + "-" + randomSuffix()
	}
	cfg.Shards = cmpDefault(cfg.Shards, DefaultShards)
	cfg.Workers = cmpDefault(cfg.Workers, DefaultWorkers)
	cfg.LeaseTTL = cmpDefault(cfg.LeaseTTL, DefaultLeaseTTL)
	cfg.SyncInterval = cmpDefault(cfg.SyncInterval, DefaultSyncInterval)
	cfg.ResyncInterval = cmpDefault(cfg.ResyncInterval, DefaultResyncInterval)
	if cfg.SyncInterval >= cfg.LeaseTTL-safetyMargin {
		return nil, errors.New("reconcile: sync interval must be well below the lease TTL")
	}
	return &Controller{
		cfg:   cfg,
		queue: newQueue(cfg.Clock),
		leases: &leaser{pool: cfg.Pool, controller: cfg.Name, member: cfg.Member,
			shards: cfg.Shards, ttl: cfg.LeaseTTL, clock: cfg.Clock},
		tracer: otel.Tracer("github.com/mindevis/qstack/pkg/reconcile"),
	}, nil
}

func cmpDefault[T int | time.Duration](v, def T) T {
	if v <= 0 {
		return def
	}
	return v
}

// Member is this replica's identity in controller_leases.
func (c *Controller) Member() string { return c.cfg.Member }

// Owns reports whether this replica currently handles id.
func (c *Controller) Owns(id string) bool { return c.leases.owns(ShardOf(id, c.cfg.Shards)) }

// Shards returns the shards this replica holds.
func (c *Controller) Shards() []int { return c.leases.ownedShards() }

// Enqueue schedules id for reconciliation if it belongs to this replica's
// shards; others are left to their owners. Event sources must therefore
// deliver to every replica (a per-replica consumer, not a shared one).
func (c *Controller) Enqueue(id string) {
	if c.Owns(id) {
		c.queue.Add(id)
	}
}

// Run runs the controller until ctx is cancelled; on return it releases its
// leases so other replicas take over at once.
func (c *Controller) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	defer func() {
		c.queue.ShutDown()
		wg.Wait()
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := c.leases.release(rctx); err != nil {
			c.cfg.Logger.WarnContext(rctx, "reconcile: release leases", "controller", c.cfg.Name, "error", err)
		}
	}()
	for range c.cfg.Workers {
		wg.Go(func() { c.work(ctx) })
	}

	syncTick := time.NewTicker(c.cfg.SyncInterval)
	defer syncTick.Stop()
	resyncTick := time.NewTicker(c.cfg.ResyncInterval)
	defer resyncTick.Stop()
	c.syncLeases(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-syncTick.C:
			c.syncLeases(ctx)
		case <-resyncTick.C:
			c.resync(ctx, nil)
		}
	}
}

// syncLeases renews leases and resyncs shards that were just acquired.
func (c *Controller) syncLeases(ctx context.Context) {
	acquired, err := c.leases.sync(ctx)
	if c.cfg.Metrics != nil {
		c.cfg.Metrics.shards.WithLabelValues(c.cfg.Name).Set(float64(len(c.leases.ownedShards())))
	}
	if err != nil {
		if ctx.Err() == nil {
			c.cfg.Logger.WarnContext(ctx, "reconcile: lease sync failed; pausing shards", "controller", c.cfg.Name, "error", err)
		}
		return
	}
	if len(acquired) > 0 {
		c.cfg.Logger.InfoContext(ctx, "reconcile: acquired shards", "controller", c.cfg.Name, "member", c.cfg.Member, "shards", acquired)
		set := make(map[int]bool, len(acquired))
		for _, s := range acquired {
			set[s] = true
		}
		c.resync(ctx, set)
	}
}

// resync enqueues live resources of the given shards (nil = all owned).
func (c *Controller) resync(ctx context.Context, shards map[int]bool) {
	ids, err := c.cfg.Lister(ctx)
	if err != nil {
		if ctx.Err() == nil {
			c.cfg.Logger.WarnContext(ctx, "reconcile: resync list", "controller", c.cfg.Name, "error", err)
		}
		return
	}
	for _, id := range ids {
		s := ShardOf(id, c.cfg.Shards)
		if (shards == nil || shards[s]) && c.leases.owns(s) {
			c.queue.Add(id)
		}
	}
}

func (c *Controller) work(ctx context.Context) {
	for {
		id, ok := c.queue.Get()
		if !ok {
			return
		}
		c.process(ctx, id)
		c.queue.Done(id)
		if c.cfg.Metrics != nil {
			c.cfg.Metrics.depth.WithLabelValues(c.cfg.Name).Set(float64(c.queue.Len()))
		}
	}
}

// Results of one reconcile (metric label).
const (
	resultOK      = "ok"
	resultRequeue = "requeue"
	resultError   = "error"
	resultSkipped = "skipped"
)

func (c *Controller) process(ctx context.Context, id string) {
	if !c.Owns(id) {
		// The shard moved to another replica, which resyncs it.
		c.queue.Forget(id)
		c.observe(resultSkipped, 0)
		return
	}
	ctx, span := c.tracer.Start(ctx, "reconcile "+c.cfg.Name, trace.WithAttributes(
		attribute.String("qs.controller", c.cfg.Name), attribute.String("qs.resource_id", id)))
	defer span.End()

	start := c.cfg.Clock.Now()
	res, err := c.reconcile(ctx, id)
	elapsed := c.cfg.Clock.Since(start)
	switch {
	case err != nil:
		delay := c.queue.AddRateLimited(id)
		span.RecordError(err)
		span.SetStatus(codes.Error, "reconcile failed")
		c.cfg.Logger.WarnContext(ctx, "reconcile failed", "controller", c.cfg.Name, "resource_id", id, "retry_in", delay, "error", err)
		c.observe(resultError, elapsed)
	case res.RequeueAfter > 0:
		c.queue.Forget(id)
		c.queue.AddAfter(id, res.RequeueAfter)
		c.observe(resultRequeue, elapsed)
	default:
		c.queue.Forget(id)
		c.observe(resultOK, elapsed)
	}
}

// reconcile turns a panic into an error so one bad resource does not stop
// the worker.
func (c *Controller) reconcile(ctx context.Context, id string) (res Result, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("reconcile: panic: %v", p)
		}
	}()
	return c.cfg.Reconciler.Reconcile(ctx, id)
}

func (c *Controller) observe(result string, d time.Duration) {
	if c.cfg.Metrics == nil {
		return
	}
	c.cfg.Metrics.total.WithLabelValues(c.cfg.Name, result).Inc()
	if result != resultSkipped {
		c.cfg.Metrics.duration.WithLabelValues(c.cfg.Name).Observe(d.Seconds())
	}
}

// Metrics are the controller metrics (spec 02 §9, REQ-OBS-001).
type Metrics struct {
	total    *prometheus.CounterVec
	duration *prometheus.HistogramVec
	depth    *prometheus.GaugeVec
	shards   *prometheus.GaugeVec
}

// NewMetrics registers qs_reconcile_* metrics; svc is the controller name.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		total: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "qs_reconcile_total", Help: "Reconcile runs by result (ok, requeue, error, skipped).",
		}, []string{"svc", "result"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "qs_reconcile_duration_seconds", Help: "Duration of reconcile runs.",
			Buckets: prometheus.ExponentialBuckets(0.001, 4, 10),
		}, []string{"svc"}),
		depth: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "qs_reconcile_queue_depth", Help: "Resources waiting in the work queue.",
		}, []string{"svc"}),
		shards: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "qs_reconcile_shards_owned", Help: "Shards leased by this replica.",
		}, []string{"svc"}),
	}
	reg.MustRegister(m.total, m.duration, m.depth, m.shards)
	return m
}

func randomSuffix() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
