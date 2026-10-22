//go:build integration

package outbox_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/db"
	"github.com/mindevis/qstack/pkg/events"
	"github.com/mindevis/qstack/pkg/ids"
	"github.com/mindevis/qstack/pkg/outbox"
	"github.com/mindevis/qstack/pkg/testenv"
)

var logger = slog.New(slog.DiscardHandler)

type env struct {
	pool   *pgxpool.Pool
	js     jetstream.JetStream
	src    events.Source
	stream string
	clock  *clock.Fake
	writer *outbox.Writer
}

// newEnv gives a fresh database (outbox and processed_events tables plus an
// "applied" table recording handler side effects) and a unique stream.
func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, db.Config{DSN: testenv.Postgres(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := outbox.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if _, err := events.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// No unique constraint: a handler applied twice shows up as two rows.
	if _, err := pool.Exec(ctx, `CREATE TABLE applied (event_id text NOT NULL, value text NOT NULL)`); err != nil {
		t.Fatal(err)
	}

	nc, err := nats.Connect(testenv.NATS(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	src := events.Source{Region: testenv.Name("t"), Service: "compute"}
	sc := src.Stream()
	sc.Memory = true
	if err := events.EnsureStream(ctx, js, sc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = js.DeleteStream(context.Background(), sc.Name) })

	clk := clock.NewFake(time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC))
	w, err := outbox.NewWriter(src, clk, ids.NewGenerator(clk, rand.Reader))
	if err != nil {
		t.Fatal(err)
	}
	return &env{pool: pool, js: js, src: src, stream: sc.Name, clock: clk, writer: w}
}

// write commits one resource event per value (rollback=true rolls it back).
func (e *env) write(t *testing.T, value string, rollback bool) events.Event {
	t.Helper()
	var ev events.Event
	errRollback := errors.New("rollback")
	err := db.InTx(context.Background(), e.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var err error
		ev, err = e.writer.ResourceChanged(context.Background(), tx, outbox.ResourceChange{
			Resource: "instance", Change: events.Updated, ResourceID: "ins-01m5hqpzg00000000000000000",
			ProjectID: "prj-01m5hqpzg00000000000000000", Version: 1, Data: wrapperspb.String(value),
		})
		if err != nil {
			return err
		}
		if rollback {
			return errRollback
		}
		return nil
	})
	if err != nil && !errors.Is(err, errRollback) {
		t.Fatal(err)
	}
	return ev
}

func (e *env) relay(t *testing.T, pub events.Publisher, m *outbox.Metrics) *outbox.Relay {
	t.Helper()
	r, err := outbox.NewRelay(outbox.RelayConfig{Pool: e.pool, Publisher: pub, Clock: e.clock, Logger: logger, Metrics: m, BatchSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (e *env) count(t *testing.T, sql string) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// streamValues reads every message of the stream in order.
func (e *env) streamValues(t *testing.T) []string {
	t.Helper()
	ctx := context.Background()
	cons, err := e.js.OrderedConsumer(ctx, e.stream, jetstream.OrderedConsumerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	info, err := cons.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for range info.NumPending {
		msg, err := cons.Next(jetstream.FetchMaxWait(5 * time.Second))
		if err != nil {
			t.Fatal(err)
		}
		ev, err := events.Unmarshal(msg.Data())
		if err != nil {
			t.Fatal(err)
		}
		v := &wrapperspb.StringValue{}
		if err := ev.DecodeData(v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v.GetValue())
	}
	return out
}

// consume runs a consumer that records each event in "applied" until the
// test ends.
func (e *env) consume(t *testing.T, handler events.Handler, m *events.ConsumerMetrics) {
	t.Helper()
	if handler == nil {
		handler = recordApplied
	}
	c, err := events.NewConsumer(context.Background(), e.js, events.ConsumerConfig{
		Name: "test", Stream: e.stream, Pool: e.pool, Handler: handler,
		Clock: e.clock, Logger: logger, Metrics: m, Backoff: []time.Duration{10 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
}

func recordApplied(ctx context.Context, tx pgx.Tx, ev events.Event) error {
	v := &wrapperspb.StringValue{}
	if err := ev.DecodeData(v); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO applied (event_id, value) VALUES ($1, $2)`, ev.ID, v.GetValue())
	return err
}

// eventually waits until cond holds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 500 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// REQ-PLT-090: only committed events are published, in commit order, and
// removed from the outbox.
func TestRelayPublishesCommittedEventsInOrder(t *testing.T) {
	e := newEnv(t)
	for i := range 5 {
		e.write(t, fmt.Sprint(i), false)
		e.write(t, "rolled back", true)
	}
	reg := prometheus.NewRegistry()
	m := outbox.NewMetrics(reg)
	n, err := e.relay(t, events.JetStreamPublisher(e.js), m).Flush(context.Background())
	if err != nil || n != 5 {
		t.Fatalf("Flush = %d, %v; want 5", n, err)
	}
	if got := e.streamValues(t); fmt.Sprint(got) != "[0 1 2 3 4]" {
		t.Errorf("stream = %v", got)
	}
	if c := e.count(t, `SELECT count(*) FROM outbox`); c != 0 {
		t.Errorf("%d events left in outbox", c)
	}
	if value(t, reg, "qs_outbox_published_total", nil) != 5 {
		t.Errorf("qs_outbox_published_total = %v", value(t, reg, "qs_outbox_published_total", nil))
	}
}

// crashingPublisher publishes, then fails the first n calls as if the relay
// died before deleting the row.
type crashingPublisher struct {
	events.Publisher
	crashes atomic.Int32
}

func (p *crashingPublisher) Publish(ctx context.Context, subject, msgID string, data []byte) error {
	if err := p.Publisher.Publish(ctx, subject, msgID, data); err != nil {
		return err
	}
	if p.crashes.Add(-1) >= 0 {
		return errors.New("relay killed after publish")
	}
	return nil
}

// Roadmap T-0.7 acceptance: the relay is killed between publishing and
// marking the event; the event is published again and still applied by the
// consumer exactly once — by JetStream deduplication inside its window and
// by the consumer's processed_events beyond it.
func TestRelayKilledAfterPublishDeliversOnce(t *testing.T) {
	e := newEnv(t)
	ev := e.write(t, "only once", false)

	crash := &crashingPublisher{Publisher: events.JetStreamPublisher(e.js)}
	crash.crashes.Store(1)
	if _, err := e.relay(t, crash, nil).Flush(context.Background()); err == nil {
		t.Fatal("first relay should have failed")
	}
	if c := e.count(t, `SELECT count(*) FROM outbox`); c != 1 {
		t.Fatalf("outbox has %d rows after the crash, want 1", c)
	}
	// The restarted relay publishes the same event again.
	if n, err := e.relay(t, events.JetStreamPublisher(e.js), nil).Flush(context.Background()); n != 1 || err != nil {
		t.Fatalf("second Flush = %d, %v", n, err)
	}
	if got := e.streamValues(t); len(got) != 1 {
		t.Errorf("stream holds %d copies, want 1 (JetStream dedup by event id)", len(got))
	}

	// Beyond the duplicate window JetStream keeps the copy; the consumer drops it.
	var payload []byte
	payload, _ = ev.Marshal()
	if _, err := e.js.Publish(context.Background(), e.src.ResourceSubject("instance", events.Updated), payload); err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewRegistry()
	m := events.NewConsumerMetrics(reg)
	e.consume(t, nil, m)
	eventually(t, "both copies handled", func() bool {
		return handled(t, reg, "ok")+handled(t, reg, "duplicate") == 2
	})
	if c := e.count(t, `SELECT count(*) FROM applied`); c != 1 {
		t.Errorf("event applied %d times, want 1", c)
	}
	if handled(t, reg, "duplicate") != 1 {
		t.Error("second copy not counted as duplicate")
	}
}

// failingPublisher fails on the n-th event.
type failingPublisher struct {
	events.Publisher
	calls, failAt int
}

func (p *failingPublisher) Publish(ctx context.Context, subject, msgID string, data []byte) error {
	p.calls++
	if p.calls == p.failAt {
		return errors.New("nats unavailable")
	}
	return p.Publisher.Publish(ctx, subject, msgID, data)
}

// A publish failure keeps the order: events published before it are removed,
// the failed one and later ones wait for the next attempt.
func TestPublishFailureKeepsOrder(t *testing.T) {
	e := newEnv(t)
	for i := range 5 {
		e.write(t, fmt.Sprint(i), false)
	}
	pub := &failingPublisher{Publisher: events.JetStreamPublisher(e.js), failAt: 3}
	r := e.relay(t, pub, nil)
	n, err := r.Flush(context.Background())
	if err == nil || n != 2 {
		t.Fatalf("Flush = %d, %v; want 2 and an error", n, err)
	}
	if c := e.count(t, `SELECT count(*) FROM outbox`); c != 3 {
		t.Fatalf("outbox has %d rows, want 3", c)
	}
	if n, err := r.Flush(context.Background()); n != 3 || err != nil {
		t.Fatalf("retry Flush = %d, %v", n, err)
	}
	if got := e.streamValues(t); fmt.Sprint(got) != "[0 1 2 3 4]" {
		t.Errorf("stream = %v", got)
	}
}

// Replicas of a controller do not relay concurrently.
func TestOneRelayAtATime(t *testing.T) {
	e := newEnv(t)
	e.write(t, "x", false)
	ctx := context.Background()
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(0x7173_6f75_7462_6f78)); err != nil {
		t.Fatal(err)
	}
	if n, err := e.relay(t, events.JetStreamPublisher(e.js), nil).Flush(ctx); n != 0 || err != nil {
		t.Errorf("Flush while another relay holds the lock = %d, %v; want 0, nil", n, err)
	}
}

// Run relays continuously and keeps the backlog gauges current.
func TestRunRelaysContinuously(t *testing.T) {
	e := newEnv(t)
	reg := prometheus.NewRegistry()
	m := outbox.NewMetrics(reg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	r, err := outbox.NewRelay(outbox.RelayConfig{Pool: e.pool, Publisher: events.JetStreamPublisher(e.js),
		Clock: e.clock, Logger: logger, Metrics: m, Interval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	go func() { done <- r.Run(ctx) }()
	e.consume(t, nil, nil)

	for i := range 3 {
		e.write(t, fmt.Sprint(i), false)
	}
	eventually(t, "3 events applied", func() bool { return e.count(t, `SELECT count(*) FROM applied`) == 3 })
	eventually(t, "empty backlog", func() bool { return value(t, reg, "qs_outbox_pending", nil) == 0 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("Run returned %v", err)
	}
}

// A failing handler rolls back (no side effects, not marked processed) and
// the event is redelivered until it succeeds.
func TestConsumerRetriesFailedHandler(t *testing.T) {
	e := newEnv(t)
	e.write(t, "retry me", false)
	if _, err := e.relay(t, events.JetStreamPublisher(e.js), nil).Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	reg := prometheus.NewRegistry()
	m := events.NewConsumerMetrics(reg)
	e.consume(t, func(ctx context.Context, tx pgx.Tx, ev events.Event) error {
		if err := recordApplied(ctx, tx, ev); err != nil {
			return err
		}
		if calls.Add(1) == 1 {
			return errors.New("dependency unavailable")
		}
		return nil
	}, m)
	eventually(t, "success after retry", func() bool { return handled(t, reg, "ok") == 1 })
	if c := e.count(t, `SELECT count(*) FROM applied`); c != 1 || calls.Load() != 2 {
		t.Errorf("applied %d rows in %d calls, want 1 row in 2 calls", c, calls.Load())
	}
}

// A malformed message is dropped and does not block the stream.
func TestConsumerDropsInvalidEnvelope(t *testing.T) {
	e := newEnv(t)
	if _, err := e.js.Publish(context.Background(), e.src.ResourceSubject("instance", events.Updated), []byte("not json")); err != nil {
		t.Fatal(err)
	}
	e.write(t, "valid", false)
	if _, err := e.relay(t, events.JetStreamPublisher(e.js), nil).Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	reg := prometheus.NewRegistry()
	m := events.NewConsumerMetrics(reg)
	e.consume(t, nil, m)
	eventually(t, "valid event applied", func() bool { return e.count(t, `SELECT count(*) FROM applied`) == 1 })
	if handled(t, reg, "invalid") != 1 {
		t.Error("invalid envelope not counted")
	}
}

func TestPurgeProcessed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.pool.Exec(ctx, `INSERT INTO processed_events (consumer, event_id, processed_at) VALUES ('c', 'old', $1), ('c', 'new', $2)`,
		e.clock.Now().Add(-events.ProcessedRetention-time.Second), e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if n, err := events.PurgeProcessed(ctx, e.pool, e.clock); n != 1 || err != nil {
		t.Errorf("PurgeProcessed = %d, %v", n, err)
	}
}

// value returns the metric with the given labels from g, 0 if absent.
func value(t *testing.T, g prometheus.Gatherer, name string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := g.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
	metrics:
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if labels[lp.GetName()] != lp.GetValue() {
					continue metrics
				}
			}
			if m.GetCounter() != nil {
				return m.GetCounter().GetValue()
			}
			return m.GetGauge().GetValue()
		}
	}
	return 0
}

func handled(t *testing.T, g prometheus.Gatherer, result string) float64 {
	t.Helper()
	return value(t, g, "qs_events_handled_total", map[string]string{"consumer": "test", "result": result})
}
