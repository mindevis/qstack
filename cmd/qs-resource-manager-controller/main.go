// Command qs-resource-manager-controller runs the background work of
// Resource Manager: it publishes outbox events to NATS JetStream,
// reconciles quota usage with usage snapshots and purges finished
// operations and reservations (spec 02 §5.3, §8; 03 §5).
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mindevis/qstack/internal/iam/catalog"
	"github.com/mindevis/qstack/internal/resourcemanager/app"
	rmconfig "github.com/mindevis/qstack/internal/resourcemanager/config"
	"github.com/mindevis/qstack/internal/resourcemanager/controller"
	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/config"
	"github.com/mindevis/qstack/pkg/db"
	"github.com/mindevis/qstack/pkg/events"
	"github.com/mindevis/qstack/pkg/ids"
	"github.com/mindevis/qstack/pkg/operation"
	"github.com/mindevis/qstack/pkg/outbox"
	"github.com/mindevis/qstack/pkg/quota"
	"github.com/mindevis/qstack/pkg/telemetry"
	"github.com/mindevis/qstack/pkg/version"
)

const binary = "qs-resource-manager-controller"

func main() {
	cfgPath := flag.String("config", "/etc/qstack/"+binary+".yaml", "configuration file")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.String(binary))
		return
	}
	if err := run(*cfgPath); err != nil {
		fmt.Fprintln(os.Stderr, binary+":", err)
		os.Exit(1)
	}
}

func run(cfgPath string) error {
	cfg := rmconfig.DefaultController()
	if err := config.Load(cfgPath, &cfg); err != nil {
		return err
	}
	level, err := telemetry.ParseLevel(cfg.Log.Level)
	if err != nil {
		return err
	}
	logger := telemetry.NewLogger(telemetry.LogOptions{Service: binary, Level: level})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dbCfg, err := cfg.Database.DB(binary)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, dbCfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	nc, js, err := events.Connect(cfg.NATSURL, binary)
	if err != nil {
		return err
	}
	defer nc.Close()
	stream := app.Source.Stream()
	stream.Replicas = cfg.StreamReplicas
	if err := events.EnsureStream(ctx, js, stream); err != nil {
		return err
	}

	reg := telemetry.NewRegistry()
	reg.MustRegister(db.NewPoolCollector(pool))
	clk := clock.Real()
	gen := ids.NewGenerator(clk, rand.Reader)
	relay, err := outbox.NewRelay(outbox.RelayConfig{
		Pool: pool, Publisher: events.JetStreamPublisher(js), Clock: clk, Logger: logger, Metrics: outbox.NewMetrics(reg),
	})
	if err != nil {
		return err
	}
	ow, err := outbox.NewWriter(app.Source, clk, gen)
	if err != nil {
		return err
	}
	ops := operation.New(pool, clk, gen)
	roles, err := catalog.Load()
	if err != nil {
		return err
	}
	a, err := app.New(app.Config{Pool: pool, Operations: ops, Outbox: ow, IDs: gen, Clock: clk, Roles: roles})
	if err != nil {
		return err
	}
	handler := controller.SnapshotHandler(a, controller.NewMetrics(reg))
	consumerMetrics := events.NewConsumerMetrics(reg)
	var consumers []*events.Consumer
	for _, region := range cfg.UsageRegions {
		us := quota.UsageStream(region)
		us.Replicas = cfg.StreamReplicas
		if err := events.EnsureStream(ctx, js, us); err != nil {
			return err
		}
		c, err := events.NewConsumer(ctx, js, events.ConsumerConfig{
			Name: controller.SnapshotConsumer, Stream: us.Name, FilterSubjects: []string{quota.SnapshotSubject(region, "*")},
			Pool: pool, Handler: handler, Clock: clk, Logger: logger, Metrics: consumerMetrics,
		})
		if err != nil {
			return err
		}
		consumers = append(consumers, c)
	}
	ready := telemetry.NewHealth(2 * time.Second)
	ready.AddCheck("db", db.Check(pool))
	ready.AddCheck("nats", func(context.Context) error {
		if !nc.IsConnected() {
			return errors.New("not connected")
		}
		return nil
	})
	admin := &http.Server{Addr: cfg.AdminAddr, Handler: ready.Handler(reg), ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 2)
	go func() { errc <- admin.ListenAndServe() }()
	go func() {
		errc <- controller.Run(ctx, controller.Config{
			Relay: relay, Operations: ops, App: a, Pool: pool, Consumers: consumers, Clock: clk, Logger: logger,
		})
	}()
	logger.InfoContext(ctx, "started", "version", version.Version, "admin_addr", cfg.AdminAddr)

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	logger.Info("stopping")
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return admin.Shutdown(shutdown)
}
