// Package api serves the Resource Manager gRPC API: request validation,
// authorization and proto mapping; the use cases live in app (REQ-PLT-071).
package api

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/proto"

	rminternalv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/internalv1"
	rmv1 "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/v1"
	"github.com/mindevis/qstack/internal/resourcemanager/app"
	"github.com/mindevis/qstack/internal/resourcemanager/store"
	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/audit"
	"github.com/mindevis/qstack/pkg/authz"
	"github.com/mindevis/qstack/pkg/filter"
	"github.com/mindevis/qstack/pkg/idempotency"
	"github.com/mindevis/qstack/pkg/operation"
	"github.com/mindevis/qstack/pkg/pagination"
	"github.com/mindevis/qstack/pkg/telemetry"
	"github.com/mindevis/qstack/pkg/validate"
)

// Public methods are callable without a subject (authz.Options.Public).
var Public = map[string]bool{
	healthpb.Health_Check_FullMethodName: true,
	healthpb.Health_Watch_FullMethodName: true,
	healthpb.Health_List_FullMethodName:  true,
}

// Options configures NewServer.
type Options struct {
	App        *app.App
	Authorizer authz.Authorizer
	// Authenticate puts the caller's authz.Subject into the context; nil
	// leaves every non-public call UNAUTHENTICATED.
	Authenticate grpc.UnaryServerInterceptor
	Auditor      *audit.Auditor
	// Operations answers repeated idempotency keys.
	Operations *operation.Store
	Pages      *pagination.Signer
	Validator  *validate.Validator
	Logger     *slog.Logger
	// Metrics is optional.
	Metrics *telemetry.GRPCMetrics
	Health  *health.Server
	// ServerOptions are appended (e.g. tracing).
	ServerOptions []grpc.ServerOption
}

// NewServer returns the public gRPC server with the platform interceptor
// chain and the Organization and Project services.
func NewServer(o Options) (*grpc.Server, error) {
	if o.App == nil || o.Authorizer == nil || o.Auditor == nil || o.Operations == nil ||
		o.Pages == nil || o.Validator == nil || o.Logger == nil || o.Health == nil {
		return nil, errors.New("api: app, authorizer, auditor, operations, pages, validator, logger and health are required")
	}
	var unary []grpc.UnaryServerInterceptor
	if o.Metrics != nil {
		unary = append(unary, o.Metrics.UnaryServerInterceptor())
	}
	unary = append(unary, apierr.UnaryServerInterceptor(apierr.Options{OnInternal: telemetry.LogInternal(o.Logger)}))
	if o.Authenticate != nil {
		unary = append(unary, o.Authenticate)
	}
	authzOpts := authz.Options{Authorizer: o.Authorizer, Public: Public, Logger: o.Logger}
	unary = append(unary,
		o.Auditor.UnaryServerInterceptor(),
		idempotency.UnaryServerInterceptor(idempotency.Options{Store: o.Operations, Subject: authz.SubjectID}),
		authz.UnaryServerInterceptor(authzOpts),
		o.Validator.UnaryServerInterceptor(),
	)
	var stream []grpc.StreamServerInterceptor
	if o.Metrics != nil {
		stream = append(stream, o.Metrics.StreamServerInterceptor())
	}
	stream = append(stream,
		apierr.StreamServerInterceptor(apierr.Options{OnInternal: telemetry.LogInternal(o.Logger)}),
		authz.StreamServerInterceptor(authzOpts),
	)
	srv := grpc.NewServer(append([]grpc.ServerOption{
		grpc.ChainUnaryInterceptor(unary...),
		grpc.ChainStreamInterceptor(stream...),
	}, o.ServerOptions...)...)
	h := &handlers{app: o.App, pages: o.Pages}
	rmv1.RegisterOrganizationServiceServer(srv, &organizations{handlers: h})
	rmv1.RegisterProjectServiceServer(srv, &projects{handlers: h})
	healthpb.RegisterHealthServer(srv, o.Health)
	return srv, nil
}

// NewInternalServer returns the server of the internal API. Serve it only
// to platform services over mTLS (REQ-SEC-001): it has no per-call
// authorization.
func NewInternalServer(a *app.App, v *validate.Validator, logger *slog.Logger, opts ...grpc.ServerOption) *grpc.Server {
	srv := grpc.NewServer(append([]grpc.ServerOption{grpc.ChainUnaryInterceptor(
		apierr.UnaryServerInterceptor(apierr.Options{OnInternal: telemetry.LogInternal(logger)}),
		v.UnaryServerInterceptor(),
	)}, opts...)...)
	rminternalv1.RegisterOrganizationServiceServer(srv, &internalOrganizations{app: a})
	return srv
}

type handlers struct {
	app   *app.App
	pages *pagination.Signer
}

// page is a decoded List request.
type page struct {
	size int
	q    store.ListQuery
}

// listPage parses paging, filter and order of a List request.
func (h *handlers) listPage(req proto.Message, pageSize int32, token, filterStr, orderBy string, schema filter.Schema) (page, error) {
	size, err := pagination.PageSize(pageSize)
	if err != nil {
		return page{}, err
	}
	f, err := filter.Parse(filterStr, schema)
	if err != nil {
		return page{}, err
	}
	order, err := pagination.ParseOrder(orderBy, store.SortFields, "create_time")
	if err != nil {
		return page{}, err
	}
	var cursor []string
	if token != "" {
		if cursor, err = h.pages.Decode(token, pagination.Binding(req)); err != nil {
			return page{}, err
		}
	}
	return page{size: size, q: store.ListQuery{Filter: f, Order: order, Cursor: cursor, Limit: size + 1}}, nil
}

// nextToken returns the token of the page after the row with cursor
// values, or "" when there are no more rows.
func (h *handlers) nextToken(req proto.Message, more bool, cursor []string) (string, error) {
	if !more {
		return "", nil
	}
	return h.pages.Encode(pagination.Binding(req), cursor)
}

// bindingsPage parses paging of ListAccessBindings (ordered by binding ID).
func (h *handlers) bindingsPage(req proto.Message, pageSize int32, token string) (size int, after string, err error) {
	if size, err = pagination.PageSize(pageSize); err != nil {
		return 0, "", err
	}
	if token == "" {
		return size, "", nil
	}
	cursor, err := h.pages.Decode(token, pagination.Binding(req))
	if err != nil {
		return 0, "", err
	}
	if len(cursor) != 1 {
		return 0, "", apierr.InvalidFields(apierr.FieldViolation{Field: "page_token", Description: "invalid page token"})
	}
	return size, cursor[0], nil
}

func subject(ctx context.Context) string { return authz.SubjectID(ctx) }
