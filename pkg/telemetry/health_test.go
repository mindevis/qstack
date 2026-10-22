package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func get(t *testing.T, srv *httptest.Server, path string) (int, string) {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// Spec 02 §13: /healthz, /readyz, /metrics.
func TestAdminEndpoints(t *testing.T) {
	h := NewHealth(time.Second)
	var natsDown atomic.Bool // written by the test, read by handler goroutines
	h.AddCheck("database", func(context.Context) error { return nil })
	h.AddCheck("nats", func(context.Context) error {
		if natsDown.Load() {
			return errors.New("nats: no servers available")
		}
		return nil
	})

	reg := NewRegistry()
	NewGRPCMetrics(reg).handled.WithLabelValues("s", "m", "OK").Inc()
	srv := httptest.NewServer(h.Handler(reg))
	defer srv.Close()

	if code, body := get(t, srv, "/healthz"); code != http.StatusOK || body != "ok\n" {
		t.Errorf("/healthz = %d %q", code, body)
	}

	code, body := get(t, srv, "/readyz")
	var rb readyBody
	_ = json.Unmarshal([]byte(body), &rb)
	if code != http.StatusOK || rb.Status != "ok" || rb.Checks["database"] != "ok" || rb.Checks["nats"] != "ok" {
		t.Errorf("/readyz (healthy) = %d %s", code, body)
	}

	natsDown.Store(true)
	code, body = get(t, srv, "/readyz")
	rb = readyBody{}
	_ = json.Unmarshal([]byte(body), &rb)
	if code != http.StatusServiceUnavailable || rb.Status != "fail" || !slices.Equal(rb.Failed, []string{"nats"}) ||
		rb.Checks["nats"] != "nats: no servers available" {
		t.Errorf("/readyz (nats down) = %d %s", code, body)
	}

	code, body = get(t, srv, "/metrics")
	if code != http.StatusOK || !strings.Contains(body, "qs_grpc_server_handled_total") || !strings.Contains(body, "go_goroutines") {
		t.Errorf("/metrics = %d, missing expected series", code)
	}

	if code, _ := get(t, srv, "/unknown"); code != http.StatusNotFound {
		t.Errorf("/unknown = %d, want 404", code)
	}
}

func TestReadyCheckTimeout(t *testing.T) {
	h := NewHealth(20 * time.Millisecond)
	h.AddCheck("slow", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	ok, res := h.Ready(context.Background())
	if ok || !strings.Contains(res["slow"], "deadline") {
		t.Errorf("Ready() = %v, %v; want timeout failure", ok, res)
	}
}

func TestReadyWithoutChecks(t *testing.T) {
	if ok, res := NewHealth(time.Second).Ready(context.Background()); !ok || len(res) != 0 {
		t.Errorf("Ready() with no checks = %v, %v", ok, res)
	}
}
