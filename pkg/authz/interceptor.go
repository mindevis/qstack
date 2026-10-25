package authz

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/protoopts"
)

// Options configures the interceptors.
type Options struct {
	Authorizer Authorizer
	// Public lists full method names callable without a subject or check:
	// sign-in, token exchange, health. Keep it minimal; it is reviewed.
	Public map[string]bool
	Logger *slog.Logger
}

// UnaryServerInterceptor authorizes public unary methods (see the package
// documentation). Install it after authentication, inside the apierr
// interceptor and inside pkg/idempotency: a replayed call returns the
// caller's own earlier Operation without reaching the handler, so it has no
// check to pass.
func UnaryServerInterceptor(opts Options) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if opts.Public[info.FullMethod] {
			return handler(ctx, req)
		}
		c, err := opts.begin(ctx, info.FullMethod)
		if err != nil {
			return nil, err
		}
		resp, err := handler(context.WithValue(ctx, callKey{}, c), req)
		return resp, opts.finish(ctx, info.FullMethod, c, err)
	}
}

// StreamServerInterceptor is UnaryServerInterceptor for streams; nothing is
// sent before an allowing check.
func StreamServerInterceptor(opts Options) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if opts.Public[info.FullMethod] {
			return handler(srv, ss)
		}
		c, err := opts.begin(ss.Context(), info.FullMethod)
		if err != nil {
			return err
		}
		ws := &checkedStream{ServerStream: ss, ctx: context.WithValue(ss.Context(), callKey{}, c), call: c, opts: &opts, method: info.FullMethod}
		return opts.finish(ss.Context(), info.FullMethod, c, handler(srv, ws))
	}
}

func (o *Options) begin(ctx context.Context, method string) (*call, error) {
	perm, ok := permissionOf(method)
	if !ok {
		o.Logger.ErrorContext(ctx, "authz: method has no permission annotation; rejecting", "method", method)
		return nil, apierr.Internal()
	}
	subject, ok := SubjectFrom(ctx)
	if !ok {
		return nil, apierr.Unauthenticated()
	}
	return &call{authz: o.Authorizer, subject: subject.ID, permission: perm, onError: func(ctx context.Context, err error) {
		o.Logger.WarnContext(ctx, "authz: access service failed", "method", method, "error", err)
	}}, nil
}

// finish enforces that the handler authorized the request.
func (o *Options) finish(ctx context.Context, method string, c *call, err error) error {
	checked, allowed := c.allowed()
	if allowed {
		return err
	}
	if err != nil {
		if checked {
			return err // the handler returns the denial or another error
		}
		switch status.Code(err) {
		case codes.InvalidArgument, codes.NotFound, codes.Unauthenticated, codes.Unavailable:
			return err // request validation or a missing resource: nothing leaks
		}
	}
	o.Logger.ErrorContext(ctx, "authz: handler did not authorize the request; response withheld",
		"method", method, "checked", checked, "handler_error", err != nil)
	return apierr.Internal()
}

type checkedStream struct {
	grpc.ServerStream
	ctx    context.Context
	call   *call
	opts   *Options
	method string
}

func (s *checkedStream) Context() context.Context { return s.ctx }

func (s *checkedStream) SendMsg(m any) error {
	if _, allowed := s.call.allowed(); !allowed {
		s.opts.Logger.ErrorContext(s.ctx, "authz: stream sent data before authorization; blocked", "method", s.method)
		return apierr.Internal()
	}
	return s.ServerStream.SendMsg(m)
}

var permissions sync.Map // full method → string ("" = none)

// permissionOf returns the (qstack.api.permission) of a registered method.
func permissionOf(fullMethod string) (string, bool) {
	if v, ok := permissions.Load(fullMethod); ok {
		return v.(string), v.(string) != ""
	}
	perm := ""
	if md := methodDescriptor(fullMethod); md != nil {
		perm, _ = protoopts.Permission(md)
	}
	permissions.Store(fullMethod, perm)
	return perm, perm != ""
}

func methodDescriptor(fullMethod string) protoreflect.MethodDescriptor {
	svc, method, ok := strings.Cut(strings.TrimPrefix(fullMethod, "/"), "/")
	if !ok {
		return nil
	}
	d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(svc))
	if err != nil {
		return nil
	}
	sd, ok := d.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil
	}
	return sd.Methods().ByName(protoreflect.Name(method))
}

// ServiceInfoProvider is satisfied by *grpc.Server.
type ServiceInfoProvider interface {
	GetServiceInfo() map[string]grpc.ServiceInfo
}

// CheckServer returns one problem per method registered on a public server
// that is neither in public nor annotated with a valid permission. Every
// service's tests call it on its real server, so a method without a
// permission fails the build (spec 03 §2).
func CheckServer(srv ServiceInfoProvider, public map[string]bool) []string {
	var problems []string
	for svc, info := range srv.GetServiceInfo() {
		for _, m := range info.Methods {
			full := "/" + svc + "/" + m.Name
			if public[full] {
				continue
			}
			md := methodDescriptor(full)
			if md == nil {
				problems = append(problems, full+": no proto descriptor registered")
				continue
			}
			if p := protoopts.CheckServices(md.Parent().(protoreflect.ServiceDescriptor)); len(p) > 0 {
				for _, msg := range p {
					if strings.HasPrefix(msg, string(md.FullName())+":") {
						problems = append(problems, msg)
					}
				}
			}
		}
	}
	sort.Strings(problems)
	return problems
}

// String is for logs.
func (p Path) String() string {
	parts := make([]string, len(p))
	for i, r := range p {
		parts[i] = fmt.Sprintf("%s/%s", r.Type, r.ID)
	}
	return strings.Join(parts, "/")
}
