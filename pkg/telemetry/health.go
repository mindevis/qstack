package telemetry

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// CheckFunc reports whether a dependency (database, NATS, ...) is usable.
type CheckFunc func(ctx context.Context) error

// Health aggregates readiness checks and serves the admin endpoints of
// spec 02 §13: /healthz (liveness), /readyz (readiness) and /metrics.
type Health struct {
	timeout time.Duration

	mu     sync.RWMutex
	checks map[string]CheckFunc
}

// NewHealth returns a Health whose checks each run with the given timeout.
func NewHealth(timeout time.Duration) *Health {
	return &Health{timeout: timeout, checks: map[string]CheckFunc{}}
}

// AddCheck registers a readiness check under name.
func (h *Health) AddCheck(name string, fn CheckFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks[name] = fn
}

// Ready runs all checks concurrently and returns whether all passed, with
// "ok" or the error text per check.
func (h *Health) Ready(ctx context.Context) (bool, map[string]string) {
	h.mu.RLock()
	checks := make(map[string]CheckFunc, len(h.checks))
	for n, fn := range h.checks {
		checks[n] = fn
	}
	h.mu.RUnlock()

	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		ok      = true
		results = make(map[string]string, len(checks))
	)
	for name, fn := range checks {
		wg.Go(func() {
			cctx, cancel := context.WithTimeout(ctx, h.timeout)
			defer cancel()
			res := "ok"
			if err := fn(cctx); err != nil {
				res = err.Error()
			}
			mu.Lock()
			defer mu.Unlock()
			results[name] = res
			if res != "ok" {
				ok = false
			}
		})
	}
	wg.Wait()
	return ok, results
}

type readyBody struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
	Failed []string          `json:"failed,omitempty"`
}

// Handler serves /healthz, /readyz and /metrics (from gatherer). It is meant
// for the admin port only, never for the public API.
func (h *Health) Handler(gatherer prometheus.Gatherer) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ok, results := h.Ready(r.Context())
		body := readyBody{Status: "ok", Checks: results}
		code := http.StatusOK
		if !ok {
			body.Status, code = "fail", http.StatusServiceUnavailable
			for n, res := range results {
				if res != "ok" {
					body.Failed = append(body.Failed, n)
				}
			}
			sort.Strings(body.Failed)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(body)
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}))
	return mux
}

// SyncGRPC keeps the overall ("") status of the gRPC health server in line
// with readiness until ctx is done, re-checking every interval.
func (h *Health) SyncGRPC(ctx context.Context, srv *health.Server, interval time.Duration) {
	update := func() {
		st := healthpb.HealthCheckResponse_NOT_SERVING
		if ok, _ := h.Ready(ctx); ok {
			st = healthpb.HealthCheckResponse_SERVING
		}
		srv.SetServingStatus("", st)
	}
	update()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			srv.Shutdown() // NOT_SERVING for everyone: we are going away
			return
		case <-t.C:
			update()
		}
	}
}
