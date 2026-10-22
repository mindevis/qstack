package events

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/db"
)

// VersionTable tracks this package's migrations separately from the service's.
const VersionTable = "qs_events_db_version"

// ProcessedRetention is how long handled event IDs are remembered; longer
// than stream retention, so a redelivered event is always recognised.
const ProcessedRetention = DefaultMaxAge + 24*time.Hour

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate creates or upgrades the processed_events table in a consuming
// service's database.
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	return db.MigrateTable(ctx, pool, sub, VersionTable)
}

// Handler applies an event inside tx; the event is marked processed in the
// same transaction. Returning an error rolls back and redelivers the event
// later. Handlers must tolerate events older than the state they hold
// (compare ResourceVersion).
type Handler func(ctx context.Context, tx pgx.Tx, ev Event) error

// ConsumerConfig configures a durable consumer.
type ConsumerConfig struct {
	// Name is the durable consumer name and the deduplication scope, e.g.
	// "metering-usage". Replicas of a service share it and split the load.
	Name           string
	Stream         string
	FilterSubjects []string
	Pool           *pgxpool.Pool
	Handler        Handler
	Clock          clock.Clock
	Logger         *slog.Logger
	Metrics        *ConsumerMetrics // optional
	// AckWait bounds one handler run before redelivery; default 30s.
	AckWait time.Duration
	// Backoff are redelivery delays after failures, the last one repeating;
	// default 1s, 5s, 30s, 1m, 5m.
	Backoff []time.Duration
}

var defaultBackoff = []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, time.Minute, 5 * time.Minute}

// Consumer processes events of a durable JetStream consumer exactly once
// with respect to the consumer's database.
type Consumer struct {
	cfg  ConsumerConfig
	cons jetstream.Consumer
}

// NewConsumer creates or updates the durable consumer.
func NewConsumer(ctx context.Context, js jetstream.JetStream, cfg ConsumerConfig) (*Consumer, error) {
	if cfg.Name == "" || cfg.Stream == "" || cfg.Pool == nil || cfg.Handler == nil || cfg.Clock == nil || cfg.Logger == nil {
		return nil, errors.New("events: consumer name, stream, pool, handler, clock and logger are required")
	}
	if cfg.AckWait == 0 {
		cfg.AckWait = 30 * time.Second
	}
	if len(cfg.Backoff) == 0 {
		cfg.Backoff = defaultBackoff
	}
	cons, err := js.CreateOrUpdateConsumer(ctx, cfg.Stream, jetstream.ConsumerConfig{
		Durable:        cfg.Name,
		FilterSubjects: cfg.FilterSubjects,
		DeliverPolicy:  jetstream.DeliverAllPolicy,
		AckPolicy:      jetstream.AckExplicitPolicy,
		AckWait:        cfg.AckWait,
		MaxDeliver:     -1,
	})
	if err != nil {
		return nil, fmt.Errorf("events: consumer %s: %w", cfg.Name, err)
	}
	return &Consumer{cfg: cfg, cons: cons}, nil
}

// Run handles events until ctx is cancelled.
func (c *Consumer) Run(ctx context.Context) error {
	it, err := c.cons.Messages(jetstream.PullMaxMessages(64))
	if err != nil {
		return fmt.Errorf("events: consumer %s: %w", c.cfg.Name, err)
	}
	stop := context.AfterFunc(ctx, it.Stop)
	defer stop()
	defer it.Stop()
	for {
		msg, err := it.Next()
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case errors.Is(err, jetstream.ErrMsgIteratorClosed):
			return errors.New("events: message iterator closed")
		case err != nil:
			c.cfg.Logger.WarnContext(ctx, "events: next message", "consumer", c.cfg.Name, "error", err)
			continue
		}
		c.handle(ctx, msg)
	}
}

// Results of handling one message (metric label).
const (
	resultOK        = "ok"
	resultDuplicate = "duplicate"
	resultError     = "error"
	resultInvalid   = "invalid"
)

func (c *Consumer) handle(ctx context.Context, msg jetstream.Msg) {
	ev, err := Unmarshal(msg.Data())
	if err != nil {
		// Redelivery cannot fix a malformed envelope.
		c.cfg.Logger.ErrorContext(ctx, "events: dropping invalid event", "consumer", c.cfg.Name, "subject", msg.Subject(), "error", err)
		_ = msg.TermWithReason("invalid envelope")
		c.observe(resultInvalid)
		return
	}
	result, err := c.Process(ctx, ev)
	if err != nil {
		delay := c.cfg.Backoff[len(c.cfg.Backoff)-1]
		if md, mdErr := msg.Metadata(); mdErr == nil && md.NumDelivered > 0 && md.NumDelivered <= uint64(len(c.cfg.Backoff)) {
			delay = c.cfg.Backoff[md.NumDelivered-1]
		}
		c.cfg.Logger.WarnContext(ctx, "events: handler failed, will retry", "consumer", c.cfg.Name,
			"event_id", ev.ID, "type", ev.Type, "retry_in", delay, "error", err)
		_ = msg.NakWithDelay(delay)
		c.observe(resultError)
		return
	}
	if err := msg.Ack(); err != nil {
		// Redelivered later and recognised as a duplicate.
		c.cfg.Logger.WarnContext(ctx, "events: ack", "consumer", c.cfg.Name, "event_id", ev.ID, "error", err)
	}
	c.observe(result)
}

// Process applies ev once: in one transaction it marks the event processed
// and runs the handler; an event seen before is skipped.
func (c *Consumer) Process(ctx context.Context, ev Event) (result string, err error) {
	result = resultOK
	err = db.InTx(ctx, c.cfg.Pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO processed_events (consumer, event_id, processed_at) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, c.cfg.Name, ev.ID, c.cfg.Clock.Now())
		if err != nil {
			return fmt.Errorf("events: mark processed: %w", err)
		}
		if tag.RowsAffected() == 0 {
			result = resultDuplicate
			return nil
		}
		return c.cfg.Handler(ctx, tx, ev)
	})
	return result, err
}

func (c *Consumer) observe(result string) {
	if c.cfg.Metrics != nil {
		c.cfg.Metrics.handled.WithLabelValues(c.cfg.Name, result).Inc()
	}
}

// PurgeProcessed forgets event IDs older than ProcessedRetention.
func PurgeProcessed(ctx context.Context, pool *pgxpool.Pool, clk clock.Clock) (int64, error) {
	tag, err := pool.Exec(ctx, `DELETE FROM processed_events WHERE processed_at < $1`, clk.Now().Add(-ProcessedRetention))
	if err != nil {
		return 0, fmt.Errorf("events: purge processed: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ConsumerMetrics counts handled events.
type ConsumerMetrics struct {
	handled *prometheus.CounterVec
}

// NewConsumerMetrics registers qs_events_handled_total{consumer,result}.
func NewConsumerMetrics(reg prometheus.Registerer) *ConsumerMetrics {
	m := &ConsumerMetrics{handled: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "qs_events_handled_total",
		Help: "Events handled by consumers, by result (ok, duplicate, error, invalid).",
	}, []string{"consumer", "result"})}
	reg.MustRegister(m.handled)
	return m
}
