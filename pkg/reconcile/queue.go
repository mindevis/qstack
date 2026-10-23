package reconcile

import (
	"sync"
	"time"

	"github.com/mindevis/qstack/pkg/clock"
)

const (
	baseBackoff = 5 * time.Millisecond
	maxBackoff  = 5 * time.Minute
)

// queue is a work queue of resource IDs: a key is queued at most once, is
// never processed by two workers at a time, and a key added while it is
// being processed is processed again afterwards.
type queue struct {
	mu         sync.Mutex
	cond       *sync.Cond
	items      []string            // FIFO of ready keys
	dirty      map[string]struct{} // queued or to be requeued after Done
	processing map[string]struct{}
	failures   map[string]int
	timers     map[string]delayed
	shutdown   bool
	clock      clock.Clock
	start      time.Time
}

type delayed struct {
	timer *time.Timer
	at    time.Duration // offset from queue creation
}

func newQueue(clk clock.Clock) *queue {
	q := &queue{
		clock:      clk,
		start:      clk.Now(),
		dirty:      map[string]struct{}{},
		processing: map[string]struct{}{},
		failures:   map[string]int{},
		timers:     map[string]delayed{},
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// Add queues key now.
func (q *queue) Add(key string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.addLocked(key)
}

func (q *queue) addLocked(key string) {
	if q.shutdown {
		return
	}
	if d, ok := q.timers[key]; ok {
		d.timer.Stop()
		delete(q.timers, key)
	}
	if _, ok := q.dirty[key]; ok {
		return
	}
	q.dirty[key] = struct{}{}
	if _, ok := q.processing[key]; ok {
		return // requeued by Done
	}
	q.items = append(q.items, key)
	q.cond.Signal()
}

// AddAfter queues key after d; an earlier pending deadline wins.
func (q *queue) AddAfter(key string, d time.Duration) {
	if d <= 0 {
		q.Add(key)
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.shutdown {
		return
	}
	at := q.clock.Since(q.start) + d
	if old, ok := q.timers[key]; ok {
		if old.at <= at {
			return
		}
		old.timer.Stop()
	}
	q.timers[key] = delayed{at: at, timer: time.AfterFunc(d, func() {
		q.mu.Lock()
		defer q.mu.Unlock()
		if cur, ok := q.timers[key]; ok && cur.at == at {
			delete(q.timers, key)
			q.addLocked(key)
		}
	})}
}

// AddRateLimited queues key after its exponential backoff and returns the delay.
func (q *queue) AddRateLimited(key string) time.Duration {
	q.mu.Lock()
	n := q.failures[key]
	q.failures[key] = n + 1
	q.mu.Unlock()
	d := backoff(n)
	q.AddAfter(key, d)
	return d
}

// backoff is 5ms·2^n capped at 5 minutes.
func backoff(n int) time.Duration {
	if n >= 16 { // 5ms·2^16 > 5 min
		return maxBackoff
	}
	return min(baseBackoff<<n, maxBackoff)
}

// Forget resets the backoff of key.
func (q *queue) Forget(key string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.failures, key)
}

// Get blocks until a key is ready; ok is false after shutdown.
func (q *queue) Get() (key string, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) == 0 && !q.shutdown {
		q.cond.Wait()
	}
	if q.shutdown {
		return "", false
	}
	key, q.items = q.items[0], q.items[1:]
	delete(q.dirty, key)
	q.processing[key] = struct{}{}
	return key, true
}

// Done marks key processed; a key re-added meanwhile is queued again.
func (q *queue) Done(key string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.processing, key)
	if _, ok := q.dirty[key]; ok && !q.shutdown {
		q.items = append(q.items, key)
		q.cond.Signal()
	}
}

// Len is the number of ready keys.
func (q *queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

// ShutDown wakes all workers and drops pending work.
func (q *queue) ShutDown() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.shutdown = true
	for k, d := range q.timers {
		d.timer.Stop()
		delete(q.timers, k)
	}
	q.cond.Broadcast()
}
