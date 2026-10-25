package protoopts_test

import (
	"strings"
	"testing"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	// Every public API package, so its descriptors are registered.
	_ "github.com/mindevis/qstack/gen/go/qstack/iam/v1"
	_ "github.com/mindevis/qstack/gen/go/qstack/operation/v1"
	_ "github.com/mindevis/qstack/gen/go/qstack/resourcemanager/v1"
	"github.com/mindevis/qstack/pkg/protoopts"
)

// Mutating methods that legitimately do not return an Operation.
var notOperation = map[protoreflect.FullName]string{
	// Exchanges a credential for a token; changes no resource.
	"qstack.iam.v1.TokenService.CreateToken": "token exchange",
}

func publicServices(t *testing.T) []protoreflect.ServiceDescriptor {
	t.Helper()
	var out []protoreflect.ServiceDescriptor
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		pkg := string(fd.Package())
		if !strings.HasPrefix(pkg, "qstack.") || strings.Contains(pkg, ".internal.") {
			return true
		}
		for i := range fd.Services().Len() {
			out = append(out, fd.Services().Get(i))
		}
		return true
	})
	if len(out) < 10 {
		t.Fatalf("found only %d public services", len(out))
	}
	return out
}

// Spec 03 §2: every public RPC declares exactly one well-formed permission.
func TestEveryPublicMethodHasAPermission(t *testing.T) {
	if problems := protoopts.CheckServices(publicServices(t)...); len(problems) > 0 {
		t.Errorf("methods without a valid permission:\n  %s", strings.Join(problems, "\n  "))
	}
}

// CLAUDE.md / spec 02 §5.3: mutating public methods return an Operation.
func TestMutatingMethodsReturnOperations(t *testing.T) {
	for _, sd := range publicServices(t) {
		for i := range sd.Methods().Len() {
			md := sd.Methods().Get(i)
			rule, _ := proto.GetExtension(md.Options(), annotations.E_Http).(*annotations.HttpRule)
			if rule == nil {
				t.Errorf("%s: no google.api.http mapping", md.FullName())
				continue
			}
			if rule.GetGet() != "" || notOperation[md.FullName()] != "" {
				continue
			}
			if md.Output().FullName() != "qstack.operation.v1.Operation" {
				t.Errorf("%s: mutating method returns %s, want qstack.operation.v1.Operation", md.FullName(), md.Output().FullName())
			}
		}
	}
}

// REQ-SEC-005: fields that carry secrets are marked sensitive, so logs and
// audit events never contain them.
func TestSecretFieldsAreSensitive(t *testing.T) {
	secretNames := []string{"secret", "private_key", "client_secret", "password", "jwt", "refresh_token", "iam_token", "api_key"}
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if !strings.HasPrefix(string(fd.Package()), "qstack.") {
			return true
		}
		msgs := fd.Messages()
		for i := range msgs.Len() {
			fields := msgs.Get(i).Fields()
			for j := range fields.Len() {
				f := fields.Get(j)
				for _, n := range secretNames {
					if string(f.Name()) == n && f.Kind() == protoreflect.StringKind && !protoopts.IsSensitive(f) {
						t.Errorf("%s holds a secret but is not (qstack.api.sensitive)", f.FullName())
					}
				}
			}
		}
		return true
	})
}
