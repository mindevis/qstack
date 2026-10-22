// Package outbox implements the transactional outbox (spec 02 §8,
// REQ-PLT-090): a change and its event are written in one transaction, and
// a relay running in the service's controller publishes the events to NATS
// JetStream and deletes them once acknowledged.
//
// Delivery is at least once: a relay that dies after publishing but before
// deleting publishes again. JetStream drops such repeats within its
// duplicate window (Nats-Msg-Id = event ID); events.Consumer drops the rest.
package outbox

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/proto"

	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/db"
	"github.com/mindevis/qstack/pkg/events"
	"github.com/mindevis/qstack/pkg/ids"
)

// VersionTable tracks this package's migrations separately from the service's.
const VersionTable = "qs_outbox_db_version"

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate creates or upgrades the outbox table in the service's database.
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	return db.MigrateTable(ctx, pool, sub, VersionTable)
}

// DB is satisfied by pgx.Tx (and *pgxpool.Pool, though events belong in the
// transaction of the change).
type DB interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Writer adds events to the outbox.
type Writer struct {
	src   events.Source
	clock clock.Clock
	ids   *ids.Generator
}

// NewWriter returns a Writer for events produced by src.
func NewWriter(src events.Source, clk clock.Clock, gen *ids.Generator) (*Writer, error) {
	if err := src.Validate(); err != nil {
		return nil, err
	}
	return &Writer{src: src, clock: clk, ids: gen}, nil
}

// ResourceChange describes a change of one resource.
type ResourceChange struct {
	// Resource is the resource type as a subject token, e.g. "instance".
	Resource   string
	Change     events.Change
	ResourceID string
	ProjectID  string
	// Version is the resource version after the change (etag).
	Version int64
	// Data is the full resource after the change (before it, for Deleted).
	Data proto.Message
}

// ResourceChanged adds the event of a resource change to tx:
// type qstack.<service>.<resource>.<change> on subject
// qs.<region>.<service>.<resource>.<change>.
func (w *Writer) ResourceChanged(ctx context.Context, tx DB, rc ResourceChange) (events.Event, error) {
	if !events.ValidResourceName(rc.Resource) {
		return events.Event{}, fmt.Errorf("outbox: invalid resource name %q", rc.Resource)
	}
	switch rc.Change {
	case events.Created, events.Updated, events.Deleted:
	default:
		return events.Event{}, fmt.Errorf("outbox: invalid change %q", rc.Change)
	}
	if rc.ResourceID == "" || rc.Data == nil {
		return events.Event{}, errors.New("outbox: resource id and data are required")
	}
	ev := events.Event{
		Type:            w.src.ResourceType(rc.Resource, rc.Change),
		Subject:         rc.ResourceID,
		ProjectID:       rc.ProjectID,
		ResourceVersion: rc.Version,
	}
	if err := ev.SetData(rc.Data); err != nil {
		return events.Event{}, err
	}
	return w.Add(ctx, tx, w.src.ResourceSubject(rc.Resource, rc.Change), ev)
}

// Add adds an arbitrary event (usage, audit) to tx. ID, source, time and
// spec version are filled in when empty.
func (w *Writer) Add(ctx context.Context, tx DB, subject string, ev events.Event) (events.Event, error) {
	if !strings.HasPrefix(subject, "qs.") || strings.ContainsAny(subject, "*> \t") {
		return events.Event{}, fmt.Errorf("outbox: invalid subject %q", subject)
	}
	if ev.SpecVersion == "" {
		ev.SpecVersion = events.SpecVersion
	}
	if ev.ID == "" {
		ev.ID = w.ids.New(ids.Event)
	}
	if ev.Source == "" {
		ev.Source = w.src.URI()
	}
	if ev.Time.IsZero() {
		ev.Time = w.clock.Now().UTC().Truncate(time.Microsecond)
	}
	payload, err := ev.Marshal()
	if err != nil {
		return events.Event{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO outbox (event_id, subject, payload, created_at) VALUES ($1, $2, $3, $4)`,
		ev.ID, subject, payload, ev.Time); err != nil {
		return events.Event{}, fmt.Errorf("outbox: insert: %w", err)
	}
	return ev, nil
}

// RelayConfig configures a Relay.
type RelayConfig struct {
	Pool      *pgxpool.Pool
	Publisher events.Publisher
	Clock     clock.Clock
	Logger    *slog.Logger
	Metrics   *Metrics // optional
	// BatchSize is the number of events per transaction; default 100.
	BatchSize int
	// Interval is the poll interval when the outbox is empty; default 200ms.
	Interval time.Duration
}

// Relay publishes outbox events in order.
type Relay struct{ cfg RelayConfig }

// NewRelay returns a Relay.
func NewRelay(cfg RelayConfig) (*Relay, error) {
	if cfg.Pool == nil || cfg.Publisher == nil || cfg.Clock == nil || cfg.Logger == nil {
		return nil, errors.New("outbox: pool, publisher, clock and logger are required")
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 200 * time.Millisecond
	}
	return &Relay{cfg: cfg}, nil
}

// Run relays events until ctx is cancelled. Errors (NATS or the database
// unavailable) are logged and retried; events wait in the outbox meanwhile.
func (r *Relay) Run(ctx context.Context) error {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		n, err := r.Flush(ctx)
		if err != nil && ctx.Err() == nil {
			r.cfg.Logger.WarnContext(ctx, "outbox: relay", "error", err)
		}
		next := r.cfg.Interval
		if err == nil && n == r.cfg.BatchSize {
			next = 0 // more is waiting
		}
		timer.Reset(next)
	}
}

// relayLock serialises relays of one database (all replicas of a
// controller) so that events are published in commit order of their seq.
const relayLock int64 = 0x7173_6f75_7462_6f78 // "qsoutbox"

// Flush publishes one batch and returns the number of events published. It
// returns 0 without error if another relay holds the lock.
func (r *Relay) Flush(ctx context.Context) (int, error) {
	published := 0
	var pubErr error
	err := db.InTx(ctx, r.cfg.Pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, relayLock).Scan(&locked); err != nil {
			return fmt.Errorf("outbox: lock: %w", err)
		}
		if !locked {
			return nil
		}
		rows, err := tx.Query(ctx, `SELECT seq, event_id, subject, payload FROM outbox ORDER BY seq LIMIT $1`, r.cfg.BatchSize)
		if err != nil {
			return fmt.Errorf("outbox: select: %w", err)
		}
		type item struct {
			seq         int64
			id, subject string
			payload     []byte
		}
		batch, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (item, error) {
			var it item
			err := row.Scan(&it.seq, &it.id, &it.subject, &it.payload)
			return it, err
		})
		if err != nil {
			return fmt.Errorf("outbox: select: %w", err)
		}

		var done []int64
		for _, it := range batch {
			if pubErr = r.cfg.Publisher.Publish(ctx, it.subject, it.id, it.payload); pubErr != nil {
				pubErr = fmt.Errorf("outbox: publish %s: %w", it.id, pubErr)
				break // keep order: later events wait for this one
			}
			done = append(done, it.seq)
		}
		if len(done) > 0 {
			if _, err := tx.Exec(ctx, `DELETE FROM outbox WHERE seq = ANY($1)`, done); err != nil {
				return fmt.Errorf("outbox: delete: %w", err)
			}
		}
		published = len(done)
		if r.cfg.Metrics != nil {
			r.cfg.Metrics.published.Add(float64(published))
			if pubErr != nil {
				r.cfg.Metrics.errors.Inc()
			}
		}
		r.observeBacklog(ctx, tx)
		// Commit the deletions even if a later publish failed.
		return nil
	})
	if err != nil {
		return 0, err
	}
	return published, pubErr
}

// observeBacklog updates the backlog gauges.
func (r *Relay) observeBacklog(ctx context.Context, tx pgx.Tx) {
	if r.cfg.Metrics == nil {
		return
	}
	var pending int64
	var oldest *time.Time
	if err := tx.QueryRow(ctx, `SELECT count(*), min(created_at) FROM outbox`).Scan(&pending, &oldest); err != nil {
		return
	}
	r.cfg.Metrics.pending.Set(float64(pending))
	lag := 0.0
	if oldest != nil {
		lag = r.cfg.Clock.Since(*oldest).Seconds()
	}
	r.cfg.Metrics.lag.Set(lag)
}

// Metrics are the relay's outbox metrics (REQ-OBS-001).
type Metrics struct {
	published prometheus.Counter
	errors    prometheus.Counter
	pending   prometheus.Gauge
	lag       prometheus.Gauge
}

// NewMetrics registers qs_outbox_* metrics.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		published: prometheus.NewCounter(prometheus.CounterOpts{Name: "qs_outbox_published_total", Help: "Events published from the outbox."}),
		errors:    prometheus.NewCounter(prometheus.CounterOpts{Name: "qs_outbox_publish_errors_total", Help: "Failed outbox publish attempts."}),
		pending:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "qs_outbox_pending", Help: "Events waiting in the outbox."}),
		lag:       prometheus.NewGauge(prometheus.GaugeOpts{Name: "qs_outbox_lag_seconds", Help: "Age of the oldest event waiting in the outbox."}),
	}
	reg.MustRegister(m.published, m.errors, m.pending, m.lag)
	return m
}
