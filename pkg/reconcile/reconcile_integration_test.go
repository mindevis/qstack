//go:build integration

package reconcile_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/db"
	"github.com/mindevis/qstack/pkg/reconcile"
	"github.com/mindevis/qstack/pkg/testenv"
)

var logger = slog.New(slog.DiscardHandler)

// fast timings for tests that do not measure failover.
const (
	fastTTL  = 2 * time.Second
	fastSync = 100 * time.Millisecond
)

func migratedDSN(t *testing.T) string {
	t.Helper()
	dsn := testenv.Postgres(t)
	pool := openPool(t, dsn)
	if _, err := reconcile.Migrate(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	return dsn
}

func openPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := db.Open(context.Background(), db.Config{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

type replica struct {
	c    *reconcile.Controller
	pool *pgxpool.Pool
	stop context.CancelFunc
	done chan struct{}
}

// start runs a replica with its own pool (closing it simulates losing the
// replica without a graceful release).
func start(t *testing.T, dsn string, cfg reconcile.Config) *replica {
	t.Helper()
	r := &replica{pool: openPool(t, dsn), done: make(chan struct{})}
	cfg.Pool, cfg.Clock, cfg.Logger = r.pool, clock.Real(), logger
	if cfg.Name == "" {
		cfg.Name = "test"
	}
	if cfg.Lister == nil {
		cfg.Lister = func(context.Context) ([]string, error) { return nil, nil }
	}
	if cfg.Reconciler == nil {
		cfg.Reconciler = reconcile.ReconcilerFunc(func(context.Context, string) (reconcile.Result, error) { return reconcile.Result{}, nil })
	}
	c, err := reconcile.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r.c = c
	ctx, cancel := context.WithCancel(context.Background())
	r.stop = cancel
	go func() { defer close(r.done); _ = c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-r.done })
	return r
}

func (r *replica) halt() { r.stop(); <-r.done }

func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) time.Duration {
	t.Helper()
	clk := clock.Real()
	begin := clk.Now()
	for clk.Since(begin) < timeout {
		if cond() {
			return clk.Since(begin)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", timeout, what)
	return 0
}

// balanced reports whether the replicas hold all shards, disjointly, with
// at most ceil(K/n) each.
func balanced(k int, rs ...*replica) bool {
	share := (k + len(rs) - 1) / len(rs)
	var all []int
	for _, r := range rs {
		s := r.c.Shards()
		if len(s) > share || len(s) == 0 {
			return false
		}
		all = append(all, s...)
	}
	slices.Sort(all)
	return len(slices.Compact(all)) == k && len(all) == k
}

// Spec 02 §9: replicas split the shards evenly and rebalance when one joins.
func TestReplicasShareShards(t *testing.T) {
	dsn := migratedDSN(t)
	cfg := reconcile.Config{LeaseTTL: fastTTL, SyncInterval: fastSync}
	a := start(t, dsn, cfg)
	eventually(t, 5*time.Second, "a owns all", func() bool { return len(a.c.Shards()) == reconcile.DefaultShards })
	b := start(t, dsn, cfg)
	eventually(t, 5*time.Second, "8/8", func() bool { return balanced(reconcile.DefaultShards, a, b) })
	c := start(t, dsn, cfg)
	eventually(t, 5*time.Second, "6/5/5", func() bool { return balanced(reconcile.DefaultShards, a, b, c) })
}

// Roadmap T-0.8 acceptance with production timings (TTL 15 s, sync 3 s): a
// replica that dies without releasing its leases loses its shards to the
// survivor within 20 s, and stops treating them as its own.
func TestDeadReplicaShardsMoveWithin20s(t *testing.T) {
	if testing.Short() {
		t.Skip("takes ~20 s")
	}
	dsn := migratedDSN(t)
	a := start(t, dsn, reconcile.Config{})
	b := start(t, dsn, reconcile.Config{})
	eventually(t, 30*time.Second, "8/8", func() bool { return balanced(reconcile.DefaultShards, a, b) })

	// Cut a off from the database: its next sync fails and nothing is released.
	a.pool.Close()
	took := eventually(t, 25*time.Second, "b owns all shards", func() bool { return len(b.c.Shards()) == reconcile.DefaultShards })
	if took > 20*time.Second {
		t.Errorf("failover took %v, want ≤ 20 s", took)
	}
	t.Logf("failover took %v", took.Round(100*time.Millisecond))
	if s := a.c.Shards(); len(s) != 0 {
		t.Errorf("dead replica still believes it owns %v", s)
	}
}

// A graceful stop releases leases, so the survivor takes over at once.
func TestGracefulStopHandsOver(t *testing.T) {
	dsn := migratedDSN(t)
	cfg := reconcile.Config{LeaseTTL: 10 * time.Second, SyncInterval: fastSync}
	a := start(t, dsn, cfg)
	b := start(t, dsn, cfg)
	eventually(t, 5*time.Second, "8/8", func() bool { return balanced(reconcile.DefaultShards, a, b) })
	a.halt()
	eventually(t, 2*time.Second, "b owns all shards long before the TTL", func() bool {
		return len(b.c.Shards()) == reconcile.DefaultShards
	})
}

// recorder counts reconciles per resource.
type recorder struct {
	mu    sync.Mutex
	calls map[string]int
}

func (r *recorder) Reconcile(_ context.Context, id string) (reconcile.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls[id]++
	return reconcile.Result{}, nil
}

func (r *recorder) count(id string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[id]
}

// Resync covers every live resource; events go only to the owning replica.
func TestResyncAndEnqueueRespectShards(t *testing.T) {
	dsn := migratedDSN(t)
	ids := make([]string, 200)
	for i := range ids {
		ids[i] = fmt.Sprintf("ins-%026d", i)
	}
	lister := func(context.Context) ([]string, error) { return ids, nil }
	ra, rb := &recorder{calls: map[string]int{}}, &recorder{calls: map[string]int{}}
	cfg := reconcile.Config{LeaseTTL: fastTTL, SyncInterval: fastSync, Lister: lister}
	cfg.Reconciler = ra
	a := start(t, dsn, cfg)
	cfg.Reconciler = rb
	b := start(t, dsn, cfg)
	eventually(t, 5*time.Second, "8/8", func() bool { return balanced(reconcile.DefaultShards, a, b) })
	eventually(t, 5*time.Second, "all resources reconciled", func() bool {
		for _, id := range ids {
			if ra.count(id)+rb.count(id) == 0 {
				return false
			}
		}
		return true
	})

	id := ids[7]
	before := ra.count(id) + rb.count(id)
	a.c.Enqueue(id)
	b.c.Enqueue(id)
	eventually(t, 2*time.Second, "event handled", func() bool { return ra.count(id)+rb.count(id) > before })
	time.Sleep(100 * time.Millisecond)
	if got := ra.count(id) + rb.count(id) - before; got != 1 {
		t.Errorf("event reconciled %d times, want once (by the owner)", got)
	}
	if a.c.Owns(id) == b.c.Owns(id) {
		t.Error("exactly one replica must own the resource")
	}
}

// Errors and panics are retried with backoff; RequeueAfter polls again.
func TestRetryAndRequeue(t *testing.T) {
	dsn := migratedDSN(t)
	var calls atomic.Int32
	done := make(chan struct{})
	reg := prometheus.NewRegistry()
	start(t, dsn, reconcile.Config{
		LeaseTTL: fastTTL, SyncInterval: fastSync, Metrics: reconcile.NewMetrics(reg),
		Lister: func(context.Context) ([]string, error) { return []string{"ins-retry"}, nil },
		Reconciler: reconcile.ReconcilerFunc(func(context.Context, string) (reconcile.Result, error) {
			switch calls.Add(1) {
			case 1, 2:
				return reconcile.Result{}, errors.New("driver unavailable")
			case 3:
				panic("bug")
			case 4:
				return reconcile.Result{RequeueAfter: 30 * time.Millisecond}, nil
			case 5:
				close(done)
			}
			return reconcile.Result{}, nil
		}),
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("reconciled %d times, want 5", calls.Load())
	}
	for result, want := range map[string]float64{"error": 3, "requeue": 1} {
		if got := counter(t, reg, result); got != want {
			t.Errorf("qs_reconcile_total{result=%q} = %v, want %v", result, got, want)
		}
	}
}

func counter(t *testing.T, g prometheus.Gatherer, result string) float64 {
	t.Helper()
	mfs, err := g.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "qs_reconcile_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "result" && lp.GetValue() == result {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}
