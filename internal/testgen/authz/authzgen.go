// Package authzgen generates authorization tests for every public gRPC
// method of a service (spec 14 §1, REQ-SEC-004).
//
// The tests are generated at run time from the proto descriptors of the
// methods registered on the service's gRPC server, so a new method is
// covered without any extra step — and fails until the service supplies a
// request for it. For each method four cases run against the real server
// with a fake AccessService:
//
//   - unauthenticated: UNAUTHENTICATED;
//   - no permissions: PERMISSION_DENIED or NOT_FOUND;
//   - other project: a subject holding every permission of the service on
//     another project gets NOT_FOUND, so tenants cannot even learn that a
//     resource or project exists;
//   - allowed: a subject holding exactly the method's permission (plus
//     declared Extra permissions) on the project is not refused.
//
// Create and List therefore authorize the project with
// authz.CheckResource(projectPath, "resourcemanager.projects.get").
package authzgen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/mindevis/qstack/pkg/authz"
	"github.com/mindevis/qstack/pkg/authz/authzfake"
	"github.com/mindevis/qstack/pkg/ids"
	"github.com/mindevis/qstack/pkg/protoopts"
)

// Suite describes the service under test.
type Suite struct {
	// Conn reaches Server, which runs the service's real interceptors and
	// handlers with Access as its authorizer.
	Conn   grpc.ClientConnInterface
	Server authz.ServiceInfoProvider
	// Public must equal the server's authz.Options.Public; those methods
	// are not generated.
	Public map[string]bool
	Access *authzfake.AccessService
	// As returns ctx authenticated as subject (the test's authentication).
	As func(ctx context.Context, subject string) context.Context

	// Project holds the resources requests target; OtherProject is the
	// other tenant's project. Both must exist in the service's view.
	Project, OtherProject authz.Resource
	// Requests maps full method names ("/pkg.Service/Method") to builders.
	Requests map[string]Request
	// Skip maps methods whose authorization the generic cases cannot
	// express to the reason, e.g. a List that filters instead of refusing;
	// the service tests them itself.
	Skip map[string]string
}

// Request builds a call of one method.
type Request struct {
	// Build returns a request targeting a resource in Suite.Project,
	// creating it if needed; it is called once per case.
	Build func(ctx context.Context) (proto.Message, error)
	// Extra are permissions the method legitimately checks besides its
	// own, e.g. "vpc.subnets.use"; granted in the allowed case only.
	Extra []string
}

// Case is one generated test.
type Case struct {
	Method string // full method name
	Name   string // unauthenticated, no-permissions, other-project, allowed
	Run    func(ctx context.Context) error
}

// Run runs every generated case as a subtest.
func Run(t *testing.T, s Suite) {
	t.Helper()
	cases, err := Generate(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(strings.TrimPrefix(c.Method, "/")+"/"+c.Name, func(t *testing.T) {
			if err := c.Run(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
}

// Generate returns the cases for every non-public method on s.Server. It
// fails if a method has no permission or no request builder.
func Generate(s Suite) ([]Case, error) {
	if problems := authz.CheckServer(s.Server, s.Public); len(problems) > 0 {
		return nil, fmt.Errorf("authzgen: methods without a valid permission:\n  %s", strings.Join(problems, "\n  "))
	}
	methods := map[string]protoreflect.MethodDescriptor{}
	for svc, info := range s.Server.GetServiceInfo() {
		for _, m := range info.Methods {
			full := "/" + svc + "/" + m.Name
			if !s.Public[full] && s.Skip[full] == "" {
				methods[full] = descriptor(svc, m.Name)
			}
		}
	}
	var missing []string
	allPerms := map[string]bool{}
	for full, md := range methods {
		if _, ok := s.Requests[full]; !ok {
			missing = append(missing, full)
		}
		p, _ := protoopts.Permission(md)
		allPerms[p] = true
		for _, e := range s.Requests[full].Extra {
			allPerms[e] = true
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("authzgen: no request builder for:\n  %s", strings.Join(missing, "\n  "))
	}

	names := make([]string, 0, len(methods))
	for full := range methods {
		names = append(names, full)
	}
	sort.Strings(names)
	var cases []Case
	for _, full := range names {
		md := methods[full]
		perm, _ := protoopts.Permission(md)
		req := s.Requests[full]
		call := func(ctx context.Context) error {
			msg, err := req.Build(ctx)
			if err != nil {
				return fmt.Errorf("build request: %w", err)
			}
			return invoke(ctx, s.Conn, full, md, msg)
		}
		cases = append(cases,
			Case{full, "unauthenticated", func(ctx context.Context) error {
				return expect(call(ctx), codes.Unauthenticated)
			}},
			Case{full, "no-permissions", func(ctx context.Context) error {
				return expect(call(s.As(ctx, newSubject())), codes.PermissionDenied, codes.NotFound)
			}},
			Case{full, "other-project", func(ctx context.Context) error {
				subject := newSubject()
				for p := range allPerms {
					s.Access.Grant(subject, p, s.OtherProject)
				}
				return expect(call(s.As(ctx, subject)), codes.NotFound)
			}},
			Case{full, "allowed", func(ctx context.Context) error {
				subject := newSubject()
				for _, p := range append([]string{perm}, req.Extra...) {
					s.Access.Grant(subject, p, s.Project)
				}
				err := call(s.As(ctx, subject))
				switch status.Code(err) {
				case codes.PermissionDenied, codes.NotFound, codes.Unauthenticated, codes.Internal, codes.Unknown:
					return fmt.Errorf("with %s on %s/%s: %w (does the handler check another permission? declare it in Extra)",
						perm, s.Project.Type, s.Project.ID, err)
				}
				return nil
			}},
		)
	}
	return cases, nil
}

func expect(err error, want ...codes.Code) error {
	if got := status.Code(err); !slices.Contains(want, got) {
		return fmt.Errorf("got %v (%v), want %v", got, err, want)
	}
	return nil
}

func newSubject() string { return ids.New(ids.User) }

func descriptor(svc, method string) protoreflect.MethodDescriptor {
	d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(svc))
	if err != nil {
		return nil
	}
	return d.(protoreflect.ServiceDescriptor).Methods().ByName(protoreflect.Name(method))
}

// invoke calls a unary or server-streaming method generically; for streams
// the first received message (or the error) is the result.
func invoke(ctx context.Context, conn grpc.ClientConnInterface, full string, md protoreflect.MethodDescriptor, req proto.Message) error {
	mt, err := protoregistry.GlobalTypes.FindMessageByName(md.Output().FullName())
	if err != nil {
		return err
	}
	resp := mt.New().Interface()
	if !md.IsStreamingClient() && !md.IsStreamingServer() {
		return conn.Invoke(ctx, full, req, resp)
	}
	if md.IsStreamingClient() {
		return errors.New("authzgen: client-streaming methods are not supported")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st, err := conn.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true}, full)
	if err != nil {
		return err
	}
	if err := st.SendMsg(req); err != nil {
		return err
	}
	if err := st.CloseSend(); err != nil {
		return err
	}
	if err := st.RecvMsg(resp); !errors.Is(err, io.EOF) {
		return err
	}
	return nil // the stream ended without messages: not a refusal
}
