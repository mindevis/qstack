package reconcile

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/db"
)

func TestMigrationsAreValid(t *testing.T) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ValidateMigrations(sub); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, q *queue) string {
	t.Helper()
	ch := make(chan string, 1)
	go func() {
		k, _ := q.Get()
		ch <- k
	}()
	select {
	case k := <-ch:
		return k
	case <-time.After(2 * time.Second):
		t.Fatal("queue.Get timed out")
		return ""
	}
}

// Spec 02 §9: the queue deduplicates by key.
func TestQueueDeduplicates(t *testing.T) {
	q := newQueue(clock.Real())
	q.Add("a")
	q.Add("b")
	q.Add("a")
	if q.Len() != 2 {
		t.Fatalf("Len = %d, want 2", q.Len())
	}
	if get(t, q) != "a" || get(t, q) != "b" {
		t.Error("FIFO order broken")
	}
}

// A key added while being processed is processed again after Done, never
// concurrently.
func TestQueueRequeuesKeyAddedDuringProcessing(t *testing.T) {
	q := newQueue(clock.Real())
	q.Add("a")
	k := get(t, q)
	q.Add("a")
	if q.Len() != 0 {
		t.Fatal("key being processed was handed out again")
	}
	q.Done(k)
	if get(t, q) != "a" {
		t.Error("key not requeued after Done")
	}
}

func TestQueueAddAfterEarliestWins(t *testing.T) {
	q := newQueue(clock.Real())
	q.AddAfter("a", time.Hour)
	q.AddAfter("a", 20*time.Millisecond)
	q.AddAfter("a", time.Hour)
	if get(t, q) != "a" {
		t.Fatal("delayed key not delivered")
	}
	q.Done("a")
	time.Sleep(50 * time.Millisecond)
	if q.Len() != 0 {
		t.Error("key delivered twice")
	}
	q.ShutDown()
}

// Spec 02 §9: exponential backoff 5 ms → 5 min.
func TestBackoff(t *testing.T) {
	for n, want := range map[int]time.Duration{
		0: 5 * time.Millisecond, 1: 10 * time.Millisecond, 10: 5120 * time.Millisecond,
		15: 163840 * time.Millisecond, 16: 5 * time.Minute, 1000: 5 * time.Minute,
	} {
		if got := backoff(n); got != want {
			t.Errorf("backoff(%d) = %v, want %v", n, got, want)
		}
	}
	q := newQueue(clock.Real())
	defer q.ShutDown()
	if d1, d2 := q.AddRateLimited("a"), q.AddRateLimited("a"); d1 != 5*time.Millisecond || d2 != 10*time.Millisecond {
		t.Errorf("rate limited delays %v, %v", d1, d2)
	}
	q.Forget("a")
	if d := q.AddRateLimited("a"); d != 5*time.Millisecond {
		t.Errorf("after Forget delay = %v", d)
	}
}

func TestQueueShutDownReleasesWorkers(t *testing.T) {
	q := newQueue(clock.Real())
	var wg sync.WaitGroup
	var stopped atomic.Int32
	for range 3 {
		wg.Go(func() {
			if _, ok := q.Get(); !ok {
				stopped.Add(1)
			}
		})
	}
	time.Sleep(10 * time.Millisecond)
	q.ShutDown()
	wg.Wait()
	if stopped.Load() != 3 {
		t.Errorf("%d workers stopped, want 3", stopped.Load())
	}
	q.Add("a")
	if q.Len() != 0 {
		t.Error("Add after shutdown queued work")
	}
}

// Spec 02 §9: shard = hash(id) % K, stable and roughly even.
func TestShardOf(t *testing.T) {
	const k = 16
	counts := make([]int, k)
	for i := range 16000 {
		s := ShardOf(fmt.Sprintf("ins-%026d", i), k)
		if s < 0 || s >= k {
			t.Fatalf("shard %d out of range", s)
		}
		counts[s]++
	}
	for s, n := range counts {
		if n < 800 || n > 1200 {
			t.Errorf("shard %d has %d of 16000 ids", s, n)
		}
	}
	// Pinned: changing the hash would move every resource to another shard.
	const id = "ins-01m5hqpzg00000000000000000"
	if ShardOf(id, 16) != 1 || ShardOf(id, 10) != 7 {
		t.Errorf("ShardOf(%s) = %d, %d; the hash must stay fnv-1a", id, ShardOf(id, 16), ShardOf(id, 10))
	}
}

func TestNewValidatesConfig(t *testing.T) {
	ok := Config{Name: "c", Pool: &pgxpool.Pool{}, Reconciler: ReconcilerFunc(nil), Lister: func(context.Context) ([]string, error) { return nil, nil },
		Clock: clock.Real(), Logger: slog.New(slog.DiscardHandler)}
	c, err := New(ok)
	if err != nil {
		t.Fatal(err)
	}
	if c.cfg.Shards != DefaultShards || c.cfg.LeaseTTL != DefaultLeaseTTL || c.cfg.SyncInterval != DefaultSyncInterval || c.Member() == "" {
		t.Errorf("defaults not applied: %+v", c.cfg)
	}
	bad := ok
	bad.SyncInterval = 15 * time.Second
	if _, err := New(bad); err == nil {
		t.Error("sync interval >= TTL accepted")
	}
	bad = ok
	bad.Name = ""
	if _, err := New(bad); err == nil {
		t.Error("empty name accepted")
	}
}
