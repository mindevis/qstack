// Package controller runs the background work of Resource Manager: the
// outbox relay (REQ-PLT-090) and the purge of finished operations past
// their retention (spec 02 §5.3). Resource changes complete synchronously,
// so there are no reconcilers yet.
package controller

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/mindevis/qstack/pkg/operation"
	"github.com/mindevis/qstack/pkg/outbox"
)

// Config configures Run.
type Config struct {
	Relay      *outbox.Relay
	Operations *operation.Store
	Logger     *slog.Logger
	// PurgeInterval is the pause between purges; default 10 minutes.
	PurgeInterval time.Duration
}

const purgeBatch = 1000

// Run works until ctx is cancelled.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Relay == nil || cfg.Operations == nil || cfg.Logger == nil {
		return errors.New("controller: relay, operations and logger are required")
	}
	if cfg.PurgeInterval <= 0 {
		cfg.PurgeInterval = 10 * time.Minute
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 2)
	go func() { errc <- cfg.Relay.Run(ctx) }()
	go func() { errc <- purge(ctx, cfg) }()
	err := <-errc
	cancel()
	if other := <-errc; err == nil {
		err = other
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
				if ctx.Err() == nil {
					cfg.Logger.WarnContext(ctx, "purge operations", "error", err)
				}
				break
			}
			if n < purgeBatch {
				break
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}
