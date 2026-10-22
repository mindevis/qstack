// Package telemetry wires logging, metrics, tracing and health endpoints the
// same way in every qStack binary (spec 02 §13).
package telemetry

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel/trace"

	"github.com/mindevis/qstack/pkg/apierr"
)

// LogOptions configures NewLogger.
type LogOptions struct {
	// Service is written as `svc` on every record.
	Service string
	Level   slog.Level
	// Output defaults to os.Stdout.
	Output io.Writer
}

// NewLogger returns a JSON logger with the fields of spec 02 §13:
// ts, level, msg, svc, request_id, trace_id (+ span_id) and any attributes
// added to the context with WithLogAttrs (subject, project_id, resource_id).
func NewLogger(o LogOptions) *slog.Logger {
	out := o.Output
	if out == nil {
		out = os.Stdout
	}
	h := slog.NewJSONHandler(out, &slog.HandlerOptions{
		Level: o.Level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				a.Key = "ts"
			}
			return a
		},
	})
	return slog.New(&contextHandler{Handler: h}).With(slog.String("svc", o.Service))
}

// ParseLevel parses debug, info, warn or error.
func ParseLevel(s string) (slog.Level, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(strings.ToUpper(s))); err != nil {
		return 0, fmt.Errorf("log level %q: %w", s, err)
	}
	return l, nil
}

type logAttrsKey struct{}

// WithLogAttrs returns ctx whose log records carry attrs, e.g.
// slog.String("project_id", id). Later values are appended, not replaced.
func WithLogAttrs(ctx context.Context, attrs ...slog.Attr) context.Context {
	prev, _ := ctx.Value(logAttrsKey{}).([]slog.Attr)
	merged := make([]slog.Attr, 0, len(prev)+len(attrs))
	merged = append(merged, prev...)
	merged = append(merged, attrs...)
	return context.WithValue(ctx, logAttrsKey{}, merged)
}

// contextHandler enriches records with request, trace and context attributes.
type contextHandler struct {
	slog.Handler
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := apierr.RequestID(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	if attrs, ok := ctx.Value(logAttrsKey{}).([]slog.Attr); ok {
		r.AddAttrs(attrs...)
	}
	return h.Handler.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithGroup(name)}
}

// LogInternal returns an apierr.Options.OnInternal hook that logs the hidden
// cause of a sanitized INTERNAL error with its request ID.
func LogInternal(l *slog.Logger) func(context.Context, error) {
	return func(ctx context.Context, err error) {
		l.ErrorContext(ctx, "internal error", slog.String("error", err.Error()))
	}
}
