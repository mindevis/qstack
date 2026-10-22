package events

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	// DefaultMaxAge is the stream retention (spec 02 §8).
	DefaultMaxAge = 7 * 24 * time.Hour
	// DefaultDuplicates is the JetStream deduplication window. It covers a
	// relay restart; older duplicates are dropped by Consumer.
	DefaultDuplicates = 15 * time.Minute
)

// StreamConfig is the subset of JetStream stream settings qStack uses.
type StreamConfig struct {
	Name     string
	Subjects []string
	// Replicas is 3 in the ha profile and 1 in single; 0 means 1.
	Replicas int
	// MaxAge and Duplicates default to DefaultMaxAge and DefaultDuplicates.
	MaxAge     time.Duration
	Duplicates time.Duration
	// Memory selects memory storage (tests); production streams use files.
	Memory bool
}

// EnsureStream creates the stream or updates it to cfg. Producers call it on
// start, so a stream never has to be created by hand.
func EnsureStream(ctx context.Context, js jetstream.JetStream, cfg StreamConfig) error {
	sc := jetstream.StreamConfig{
		Name:       cfg.Name,
		Subjects:   cfg.Subjects,
		Retention:  jetstream.LimitsPolicy,
		Storage:    jetstream.FileStorage,
		Replicas:   max(cfg.Replicas, 1),
		MaxAge:     cfg.MaxAge,
		Duplicates: cfg.Duplicates,
	}
	if sc.MaxAge == 0 {
		sc.MaxAge = DefaultMaxAge
	}
	if sc.Duplicates == 0 {
		sc.Duplicates = DefaultDuplicates
	}
	if cfg.Memory {
		sc.Storage = jetstream.MemoryStorage
	}
	if _, err := js.CreateOrUpdateStream(ctx, sc); err != nil {
		return fmt.Errorf("events: ensure stream %s: %w", cfg.Name, err)
	}
	return nil
}

// Connect connects to NATS and reconnects forever: losing NATS delays
// events but must not stop a service (static stability).
func Connect(url, name string, opts ...nats.Option) (*nats.Conn, jetstream.JetStream, error) {
	opts = append([]nats.Option{nats.Name(name), nats.MaxReconnects(-1), nats.RetryOnFailedConnect(true)}, opts...)
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("events: connect: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, nil, fmt.Errorf("events: jetstream: %w", err)
	}
	return nc, js, nil
}

// Publisher publishes one encoded event; msgID is the event ID.
type Publisher interface {
	Publish(ctx context.Context, subject, msgID string, data []byte) error
}

// JetStreamPublisher publishes with Nats-Msg-Id = event ID and waits for the
// stream's acknowledgement.
func JetStreamPublisher(js jetstream.JetStream) Publisher { return jsPublisher{js} }

type jsPublisher struct{ js jetstream.JetStream }

func (p jsPublisher) Publish(ctx context.Context, subject, msgID string, data []byte) error {
	_, err := p.js.Publish(ctx, subject, data, jetstream.WithMsgID(msgID))
	return err
}
