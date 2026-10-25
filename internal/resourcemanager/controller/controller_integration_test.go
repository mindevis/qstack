//go:build integration

package controller_test

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	rmv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/v1"
	"github.com/mindevis/qstack/internal/iam/catalog"
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
	"github.com/mindevis/qstack/pkg/quota"
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
	for _, m := range []func(context.Context, *pgxpool.Pool) ([]string, error){store.Migrate, operation.Migrate, outbox.Migrate, events.Migrate} {
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
	roles, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(app.Config{Pool: pool, Operations: ops, Outbox: ow, IDs: gen, Clock: clk, Roles: roles})
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
		done <- controller.Run(runCtx, controller.Config{Relay: relay, Operations: ops, App: a, Pool: pool, Clock: clk, Logger: logger})
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

// REQ-RM-002 end to end: a service's usage snapshot travels through the
// outbox and the region's usage stream and corrects drifted usage.
func TestSnapshotReconciliation(t *testing.T) {
	ctx := t.Context()
	pool, err := db.Open(ctx, db.Config{DSN: testenv.Postgres(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, m := range []func(context.Context, *pgxpool.Pool) ([]string, error){store.Migrate, operation.Migrate, outbox.Migrate, events.Migrate} {
		if _, err := m(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	nc, js, err := events.Connect(testenv.NATS(t), "rm-snapshot-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	region := testenv.Name("r")
	us := quota.UsageStream(region)
	us.Memory = true
	if err := events.EnsureStream(ctx, js, us); err != nil {
		t.Fatal(err)
	}

	clk := clock.Real()
	gen := ids.NewGenerator(clk, rand.Reader)
	ow, err := outbox.NewWriter(app.Source, clk, gen)
	if err != nil {
		t.Fatal(err)
	}
	ops := operation.New(pool, clk, gen)
	roles, err := catalog.Load()
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(app.Config{Pool: pool, Operations: ops, Outbox: ow, IDs: gen, Clock: clk, Roles: roles})
	if err != nil {
		t.Fatal(err)
	}
	org, err := a.CreateOrganization(ctx, testenv.Name("org"), "", ids.New(ids.User), domain.OrgActive)
	if err != nil {
		t.Fatal(err)
	}
	op, err := a.CreateProject(ctx, ids.New(ids.User), app.ProjectInput{OrganizationID: org.ID, Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	project := op.GetResourceId()
	res := ids.New(ids.Operation)
	if _, err := a.Reserve(ctx, project, res, map[string]int64{"compute.instances.count": 3}); err != nil {
		t.Fatal(err)
	}
	if err := a.Commit(ctx, res); err != nil {
		t.Fatal(err)
	}

	// compute counts 1 instance and publishes through its outbox (here the
	// same database: the relay publishes every outbox row).
	computeOutbox, err := outbox.NewWriter(events.Source{Region: region, Service: "compute"}, clk, gen)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTx(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		return quota.PublishSnapshot(ctx, tx, computeOutbox, region, "compute", clk.Now(),
			map[string]map[string]int64{project: {"compute.instances.count": 1}})
	}); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.DiscardHandler)
	relay, err := outbox.NewRelay(outbox.RelayConfig{Pool: pool, Publisher: events.JetStreamPublisher(js), Clock: clk, Logger: logger, Interval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := events.NewConsumer(ctx, js, events.ConsumerConfig{
		Name: controller.SnapshotConsumer, Stream: us.Name, FilterSubjects: []string{quota.SnapshotSubject(region, "*")},
		Pool: pool, Handler: controller.SnapshotHandler(a, nil), Clock: clk, Logger: logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The resource-manager stream must exist for the relay to publish its
	// own events.
	sc := app.Source.Stream()
	sc.Memory = true
	if err := events.EnsureStream(ctx, js, sc); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- controller.Run(runCtx, controller.Config{
			Relay: relay, Operations: ops, App: a, Pool: pool, Consumers: []*events.Consumer{consumer}, Clock: clk, Logger: logger,
		})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("controller stopped with %v", err)
		}
	})

	deadline := clk.Now().Add(10 * time.Second)
	for {
		var used int64
		if err := pool.QueryRow(ctx, `SELECT used FROM quota_usage WHERE project_id = $1 AND metric = 'compute.instances.count'`, project).Scan(&used); err != nil {
			t.Fatal(err)
		}
		if used == 1 {
			return
		}
		if clk.Now().After(deadline) {
			t.Fatalf("used = %d, want 1 after the snapshot", used)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
