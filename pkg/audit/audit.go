// Package audit records audit trail events (spec 11 §3, REQ-OBS-010) and
// removes sensitive fields from requests in audit events and logs
// (REQ-SEC-005).
//
// The interceptor audits every mutating call (pkg/idempotency.IsMutating),
// successful or not. Events go through the outbox, so they are not lost:
//
//   - on success the event is written in the transaction of the change, by
//     the operation.Create hook (OperationCreated), atomically with it;
//   - otherwise (denied, invalid, busy, or no operation created) the
//     interceptor writes it in a transaction of its own after the handler.
//
// Both writes carry the same event ID; if the handler committed and still
// failed, the second write hits the outbox's unique event ID and is skipped.
package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/genproto/googleapis/rpc/code"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/mindevis/qstack/gen/go/qstack/audit/v1"
	operationv1 "github.com/mindevis/qstack/gen/go/qstack/operation/v1"
	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/authz"
	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/db"
	"github.com/mindevis/qstack/pkg/events"
	"github.com/mindevis/qstack/pkg/idempotency"
	"github.com/mindevis/qstack/pkg/ids"
	"github.com/mindevis/qstack/pkg/operation"
	"github.com/mindevis/qstack/pkg/outbox"
)

// EventType is the CloudEvents type of audit events.
const EventType = "qstack.audit.event.created"

// Options configures an Auditor.
type Options struct {
	// Service names the service, e.g. "compute"; Region the region, used
	// for the subject qs.<region>.audit.<service>.
	Service, Region string
	Pool            *pgxpool.Pool
	Outbox          *outbox.Writer
	Clock           clock.Clock
	IDs             *ids.Generator
	Logger          *slog.Logger
	Metrics         *Metrics // optional
}

// Auditor records audit events.
type Auditor struct {
	opts    Options
	subject string
}

// New validates opts and returns an Auditor.
func New(opts Options) (*Auditor, error) {
	if opts.Pool == nil || opts.Outbox == nil || opts.Clock == nil || opts.IDs == nil || opts.Logger == nil {
		return nil, errors.New("audit: pool, outbox, clock, ids and logger are required")
	}
	src := events.Source{Region: opts.Region, Service: opts.Service}
	if err := src.Validate(); err != nil {
		return nil, err
	}
	return &Auditor{opts: opts, subject: "qs." + opts.Region + ".audit." + opts.Service}, nil
}

// pending is the audit state of one call.
type pending struct {
	mu      sync.Mutex
	ev      *auditv1.AuditEvent
	target  *authz.Target
	written bool // written in the handler's transaction
}

type pendingKey struct{}

// UnaryServerInterceptor audits mutating calls. Install it after
// authentication and before pkg/authz, inside the apierr interceptor.
func (a *Auditor) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		msg, ok := req.(proto.Message)
		if !ok || !idempotency.IsMutating(info.FullMethod) {
			return handler(ctx, req)
		}
		ctx, target := authz.WithTarget(ctx)
		p := &pending{ev: a.newEvent(ctx, info.FullMethod, msg), target: target}
		resp, err := handler(context.WithValue(ctx, pendingKey{}, p), req)

		p.mu.Lock()
		done := err == nil && p.written
		p.mu.Unlock()
		if !done {
			op, _ := resp.(*operationv1.Operation)
			ev := p.finish(op, err)
			if werr := a.writeOwnTx(context.WithoutCancel(ctx), ev); werr != nil {
				a.opts.Logger.ErrorContext(ctx, "audit: event lost", "event_id", ev.GetId(), "method", info.FullMethod, "error", werr)
				a.observe("error")
			} else {
				a.observe("written")
			}
		}
		return resp, err
	}
}

// OperationCreated is an operation.CreateHook: it writes the call's audit
// event in the transaction that creates the operation.
func (a *Auditor) OperationCreated(ctx context.Context, tx operation.DB, op *operationv1.Operation) error {
	p, ok := ctx.Value(pendingKey{}).(*pending)
	if !ok {
		return nil // not an audited API call (e.g. a controller)
	}
	ev := p.finish(op, nil)
	if err := a.write(ctx, tx, ev); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	p.mu.Lock()
	p.written = true
	p.mu.Unlock()
	a.observe("written")
	return nil
}

// Emit writes an explicit event (sign-in, sign-up) in tx; ID, time and
// service are filled in when empty and request is redacted.
func (a *Auditor) Emit(ctx context.Context, tx outbox.DB, ev *auditv1.AuditEvent) error {
	ev = proto.Clone(ev).(*auditv1.AuditEvent)
	if ev.GetId() == "" {
		ev.Id = a.opts.IDs.New(ids.Event)
	}
	if ev.GetTime() == nil {
		ev.Time = timestamppb.New(a.opts.Clock.Now())
	}
	if ev.GetService() == "" {
		ev.Service = a.opts.Service
	}
	return a.write(ctx, tx, ev)
}

func (a *Auditor) newEvent(ctx context.Context, method string, req proto.Message) *auditv1.AuditEvent {
	ev := &auditv1.AuditEvent{
		Id:        a.opts.IDs.New(ids.Event),
		Time:      timestamppb.New(a.opts.Clock.Now()),
		Service:   a.opts.Service,
		Method:    method,
		Request:   RedactedStruct(req),
		SourceIp:  sourceIP(ctx),
		UserAgent: userAgent(ctx),
	}
	if s, ok := authz.SubjectFrom(ctx); ok {
		ev.SubjectId, ev.SubjectType, ev.AuthMethod = s.ID, s.Type, s.AuthMethod
	}
	return ev
}

// finish returns a snapshot of the event with the outcome and targets.
func (p *pending) finish(op *operationv1.Operation, err error) *auditv1.AuditEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	ev := proto.Clone(p.ev).(*auditv1.AuditEvent)
	for _, r := range p.target.Path() {
		switch r.Type {
		case "organization":
			ev.OrganizationId = r.ID
		case "project":
			ev.ProjectId = r.ID
		default:
			ev.ResourceIds = appendUnique(ev.ResourceIds, r.ID)
		}
	}
	if op != nil {
		ev.OperationId = op.GetId()
		if op.GetProjectId() != "" {
			ev.ProjectId = op.GetProjectId()
		}
		if op.GetResourceId() != "" {
			ev.ResourceIds = appendUnique(ev.ResourceIds, op.GetResourceId())
		}
	}
	if err != nil {
		st := status.Convert(err)
		ev.StatusCode = code.Code(st.Code()) //nolint:gosec // gRPC codes are small
		if r := apierr.Reason(err); r != 0 {
			ev.StatusReason = r.String()
		}
	}
	return ev
}

func appendUnique(s []string, v string) []string {
	if slices.Contains(s, v) {
		return s
	}
	return append(s, v)
}

// writeOwnTx writes ev in a transaction of its own; an event already
// committed by the handler's transaction is not written twice.
func (a *Auditor) writeOwnTx(ctx context.Context, ev *auditv1.AuditEvent) error {
	err := db.InTx(ctx, a.opts.Pool, pgx.TxOptions{}, func(tx pgx.Tx) error { return a.write(ctx, tx, ev) })
	if db.IsUniqueViolation(err, "outbox_event_id_key") {
		return nil
	}
	return err
}

func (a *Auditor) write(ctx context.Context, tx outbox.DB, ev *auditv1.AuditEvent) error {
	data, err := protojson.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = a.opts.Outbox.Add(ctx, tx, a.subject, events.Event{
		ID:        ev.GetId(),
		Type:      EventType,
		Subject:   ev.GetId(),
		Time:      ev.GetTime().AsTime(),
		ProjectID: ev.GetProjectId(),
		Data:      data, DataContentType: "application/json",
	})
	return err
}

func sourceIP(ctx context.Context) string {
	md, _ := metadata.FromIncomingContext(ctx)
	// Set by qs-api-gateway; the first entry is the client.
	if v := md.Get("x-forwarded-for"); len(v) > 0 {
		if ip, _, _ := strings.Cut(v[0], ","); strings.TrimSpace(ip) != "" {
			return strings.TrimSpace(ip)
		}
	}
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		if host, _, err := net.SplitHostPort(p.Addr.String()); err == nil {
			return host
		}
		return p.Addr.String()
	}
	return ""
}

func userAgent(ctx context.Context) string {
	md, _ := metadata.FromIncomingContext(ctx)
	for _, k := range []string{"grpcgateway-user-agent", "user-agent"} {
		if v := md.Get(k); len(v) > 0 {
			return truncate(v[0], 256)
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func (a *Auditor) observe(result string) {
	if a.opts.Metrics != nil {
		a.opts.Metrics.events.WithLabelValues(result).Inc()
	}
}

// Metrics counts audit writes; result="error" means an event was lost and
// must alert.
type Metrics struct{ events *prometheus.CounterVec }

// NewMetrics registers qs_audit_events_total{result}.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{events: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "qs_audit_events_total", Help: "Audit events written (written) or lost (error).",
	}, []string{"result"})}
	reg.MustRegister(m.events)
	return m
}
