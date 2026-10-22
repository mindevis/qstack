package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/mindevis/qstack/pkg/apierr"
)

func decodeLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("log line is not JSON: %v\n%s", err, buf.String())
	}
	return m
}

// Spec 02 §13 log fields; REQ-PLT-061: request_id in logs.
func TestLoggerFields(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(LogOptions{Service: "qs-test", Level: slog.LevelInfo, Output: &buf})

	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()
	ctx = apierr.WithRequestID(ctx, "req-123")
	ctx = WithLogAttrs(ctx, slog.String("subject", "usr-1"), slog.String("project_id", "prj-1"))
	ctx = WithLogAttrs(ctx, slog.String("resource_id", "ins-1"))

	log.InfoContext(ctx, "instance created", slog.Int("vcpus", 2))

	m := decodeLine(t, &buf)
	want := map[string]any{
		"msg": "instance created", "level": "INFO", "svc": "qs-test", "request_id": "req-123",
		"trace_id": span.SpanContext().TraceID().String(), "span_id": span.SpanContext().SpanID().String(),
		"subject": "usr-1", "project_id": "prj-1", "resource_id": "ins-1", "vcpus": float64(2),
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	if _, ok := m["ts"]; !ok {
		t.Error("missing ts")
	}
	if _, ok := m["time"]; ok {
		t.Error("time must be renamed to ts")
	}
}

func TestLoggerWithoutContextValues(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(LogOptions{Service: "qs-test", Output: &buf}).With(slog.String("component", "x"))
	log.Info("plain")
	m := decodeLine(t, &buf)
	for _, k := range []string{"request_id", "trace_id", "project_id"} {
		if _, ok := m[k]; ok {
			t.Errorf("unexpected %s in %v", k, m)
		}
	}
	if m["component"] != "x" || m["svc"] != "qs-test" {
		t.Errorf("With() attributes lost: %v", m)
	}
}

func TestLoggerLevel(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(LogOptions{Service: "s", Level: slog.LevelWarn, Output: &buf})
	log.Info("hidden")
	if buf.Len() != 0 {
		t.Errorf("info logged at warn level: %s", buf.String())
	}
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError} {
		if got, err := ParseLevel(in); err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseLevel("loud"); err == nil {
		t.Error("ParseLevel(loud) succeeded")
	}
}

func TestLogInternal(t *testing.T) {
	var buf bytes.Buffer
	hook := LogInternal(NewLogger(LogOptions{Service: "s", Output: &buf}))
	hook(apierr.WithRequestID(context.Background(), "req-9"), errors.New("pq: connection refused"))
	m := decodeLine(t, &buf)
	if m["level"] != "ERROR" || m["request_id"] != "req-9" || m["error"] != "pq: connection refused" {
		t.Errorf("unexpected record: %v", m)
	}
}
