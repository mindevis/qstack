// Command qs-resource-manager-api serves the public Resource Manager API
// (spec 03 §6): organizations, projects and their access bindings.
//
// Authentication of end users (IAM tokens from qs-api-gateway) arrives with
// T-1.5/T-1.11; until then every call is UNAUTHENTICATED. The internal API
// (CreateOrganization for sign-up) is not served yet.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"

	"github.com/mindevis/qstack/internal/iam/catalog"
	"github.com/mindevis/qstack/internal/resourcemanager/api"
	"github.com/mindevis/qstack/internal/resourcemanager/app"
	rmconfig "github.com/mindevis/qstack/internal/resourcemanager/config"
	"github.com/mindevis/qstack/internal/resourcemanager/store"
	"github.com/mindevis/qstack/pkg/audit"
	"github.com/mindevis/qstack/pkg/authz"
	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/config"
	"github.com/mindevis/qstack/pkg/db"
	"github.com/mindevis/qstack/pkg/events"
	"github.com/mindevis/qstack/pkg/grpcclient"
	"github.com/mindevis/qstack/pkg/ids"
	"github.com/mindevis/qstack/pkg/operation"
	"github.com/mindevis/qstack/pkg/outbox"
	"github.com/mindevis/qstack/pkg/pagination"
	"github.com/mindevis/qstack/pkg/telemetry"
	"github.com/mindevis/qstack/pkg/validate"
	"github.com/mindevis/qstack/pkg/version"
)

const binary = "qs-resource-manager-api"

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
	cfg := rmconfig.DefaultAPI()
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

	flushTraces, err := telemetry.SetupTracing(ctx, telemetry.TracingOptions{
		Service: binary, Version: version.Version, Endpoint: cfg.Telemetry.OTLPEndpoint,
		Insecure: cfg.Telemetry.OTLPInsecure, SampleRatio: cfg.Telemetry.SampleRatio,
	})
	if err != nil {
		return err
	}
	defer func() { _ = flushTraces(context.Background()) }()

	dbCfg, err := cfg.Database.DB(binary)
	if err != nil {
		return err
	}
	pool, err := db.Open(ctx, dbCfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	if cfg.Migrate {
		for _, m := range []func(context.Context, *pgxpool.Pool) ([]string, error){store.Migrate, operation.Migrate, outbox.Migrate, events.Migrate} {
			if _, err := m(ctx, pool); err != nil {
				return fmt.Errorf("migrate: %w", err)
			}
		}
	}

	reg := telemetry.NewRegistry()
	reg.MustRegister(db.NewPoolCollector(pool))
	clk := clock.Real()
	gen := ids.NewGenerator(clk, rand.Reader)
	ow, err := outbox.NewWriter(app.Source, clk, gen)
	if err != nil {
		return err
	}
	auditor, err := audit.New(audit.Options{
		Service: app.Source.Service, Region: app.Source.Region, Pool: pool, Outbox: ow,
		Clock: clk, IDs: gen, Logger: logger, Metrics: audit.NewMetrics(reg),
	})
	if err != nil {
		return err
	}
	ops := operation.New(pool, clk, gen, operation.WithCreateHook(auditor.OperationCreated))
	roles, err := catalog.Load()
	if err != nil {
		return err
	}
	a, err := app.New(app.Config{Pool: pool, Operations: ops, Outbox: ow, IDs: gen, Clock: clk, Roles: roles, QuotaDefaults: cfg.Quotas()})
	if err != nil {
		return err
	}

	clientTLS, err := cfg.TLS.ClientTLS()
	if err != nil {
		return err
	}
	serverTLS, err := cfg.TLS.ServerTLS()
	if err != nil {
		return err
	}
	accessConn, err := grpcclient.New(cfg.AccessAddr, grpcclient.Options{TLS: clientTLS})
	if err != nil {
		return err
	}
	defer func() { _ = accessConn.Close() }()
	keys, err := cfg.PageKeys()
	if err != nil {
		return err
	}
	pages, err := pagination.NewSigner(clk, keys...)
	if err != nil {
		return err
	}
	validator, err := validate.New()
	if err != nil {
		return err
	}

	ready := telemetry.NewHealth(2 * time.Second)
	ready.AddCheck("db", db.Check(pool))
	grpcHealth := health.NewServer()
	srv, err := api.NewServer(api.Options{
		App: a, Authorizer: authz.NewClient(accessConn, authz.ClientOptions{Clock: clk}),
		Auditor: auditor, Operations: ops, Pages: pages, Validator: validator, Logger: logger,
		Metrics: telemetry.NewGRPCMetrics(reg), Health: grpcHealth,
		ServerOptions: []grpc.ServerOption{grpc.Creds(credentials.NewTLS(serverTLS)), telemetry.GRPCServerTracing()},
	})
	if err != nil {
		return err
	}
	go ready.SyncGRPC(ctx, grpcHealth, 5*time.Second)

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return err
	}
	admin := &http.Server{Addr: cfg.AdminAddr, Handler: ready.Handler(reg), ReadHeaderTimeout: 5 * time.Second}
	errc := make(chan error, 2)
	go func() { errc <- srv.Serve(lis) }()
	go func() { errc <- admin.ListenAndServe() }()
	logger.InfoContext(ctx, "started", "version", version.Version, "grpc_addr", cfg.GRPCAddr, "admin_addr", cfg.AdminAddr)

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	logger.Info("stopping")
	stopped := make(chan struct{})
	go func() { srv.GracefulStop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(20 * time.Second):
		srv.Stop()
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return admin.Shutdown(shutdown)
}
