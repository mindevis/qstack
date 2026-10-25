//go:build integration

package controller_test

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	rmv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/v1"
	"github.com/mindevis/qstack/internal/resourcemanager/app"
	"github.com/mindevis/qstack/internal/resourcemanager/controller"
	"github.com/mindevis/qstack/internal/resourcemanager/domain"
	"github.com/mindevis/qstack/internal/resourcemanager/store"
	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/db"
	"github.com/mindevis/qstack/pkg/events"
	"github.com/mindevis/qstack/pkg/ids"
	"github.com/mindevis/qstack/pkg/operation"
	"github.com/mindevis/qstack/pkg/outbox"
	"github.com/mindevis/qstack/pkg/testenv"
)

// REQ-PLT-090: changes of Resource Manager reach its JetStream stream.
func TestRelaysEvents(t *testing.T) {
	ctx := t.Context()
	pool, err := db.Open(ctx, db.Config{DSN: testenv.Postgres(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, m := range []func(context.Context, *pgxpool.Pool) ([]string, error){store.Migrate, operation.Migrate, outbox.Migrate} {
		if _, err := m(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	nc, js, err := events.Connect(testenv.NATS(t), "rm-controller-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	sc := app.Source.Stream()
	sc.Memory = true
	if err := events.EnsureStream(ctx, js, sc); err != nil {
		t.Fatal(err)
	}

	clk := clock.Real()
	gen := ids.NewGenerator(clk, rand.Reader)
	ow, err := outbox.NewWriter(app.Source, clk, gen)
	if err != nil {
		t.Fatal(err)
	}
	ops := operation.New(pool, clk, gen)
	a, err := app.New(app.Config{Pool: pool, Operations: ops, Outbox: ow, IDs: gen, Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	org, err := a.CreateOrganization(ctx, testenv.Name("org"), "", ids.New(ids.User), domain.OrgActive)
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.DiscardHandler)
	relay, err := outbox.NewRelay(outbox.RelayConfig{Pool: pool, Publisher: events.JetStreamPublisher(js), Clock: clk, Logger: logger, Interval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- controller.Run(runCtx, controller.Config{Relay: relay, Operations: ops, Logger: logger})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("controller stopped with %v", err)
		}
	})

	stream, err := js.Stream(ctx, sc.Name)
	if err != nil {
		t.Fatal(err)
	}
	subject := "qs.global.resourcemanager.organization.created"
	deadline := clk.Now().Add(10 * time.Second)
	for {
		msg, err := stream.GetLastMsgForSubject(ctx, subject)
		if err == nil {
			ev, err := events.Unmarshal(msg.Data)
			if err != nil {
				t.Fatal(err)
			}
			got := &rmv1.Organization{}
			if err := ev.DecodeData(got); err != nil {
				t.Fatal(err)
			}
			if got.GetId() == org.ID {
				if ev.Type != "qstack.resourcemanager.organization.created" {
					t.Errorf("event type %q", ev.Type)
				}
				break
			}
		}
		if clk.Now().After(deadline) {
			t.Fatalf("no %s event for %s: %v", subject, org.ID, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := stream.GetLastMsgForSubject(ctx, "qs.global.resourcemanager.accessbindings.updated"); err != nil {
		t.Errorf("owner binding event: %v", err)
	}
}
