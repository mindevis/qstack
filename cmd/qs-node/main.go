// Command qs-node runs on every compute node (spec 15 §2): later it serves
// the console and API proxy on :8443, joins the node and starts the agents
// (T-1.68). For now it is the packaging skeleton: configuration, structured
// logs, health and metrics on the admin address, and systemd readiness.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mindevis/qstack/pkg/config"
	"github.com/mindevis/qstack/pkg/telemetry"
	"github.com/mindevis/qstack/pkg/version"
)

const binary = "qs-node"

// Config is /etc/qstack/qs-node.yaml.
type Config struct {
	config.Common `yaml:",inline"`
}

func main() {
	cfgPath := flag.String("config", "/etc/qstack/qs-node.yaml", "configuration file")
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
	cfg := Config{Common: config.DefaultCommon(binary)}
	cfg.AdminAddr = "127.0.0.1:9101" // node-local; scraped through the node exporter path
	if err := config.Load(cfgPath, &cfg); err != nil {
		return err
	}
	level, err := telemetry.ParseLevel(cfg.Log.Level)
	if err != nil {
		return err
	}
	logger := telemetry.NewLogger(telemetry.LogOptions{Service: binary, Level: level})

	reg := telemetry.NewRegistry()
	health := telemetry.NewHealth(2 * time.Second)
	admin := &http.Server{Addr: cfg.AdminAddr, Handler: health.Handler(reg), ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- admin.ListenAndServe() }()

	logger.InfoContext(ctx, "started", "version", version.Version, "admin_addr", cfg.AdminAddr)
	if _, err := telemetry.SDNotify("READY=1"); err != nil {
		logger.WarnContext(ctx, "sd_notify failed", "error", err)
	}

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("admin server: %w", err)
		}
	}
	_, _ = telemetry.SDNotify("STOPPING=1")
	logger.Info("stopping")
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return admin.Shutdown(shutdown)
}
