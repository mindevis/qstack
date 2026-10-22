package protoopts

import (
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	_ "google.golang.org/protobuf/types/known/emptypb" // registers google/protobuf/empty.proto

	apipb "github.com/mindevis/qstack/gen/go/qstack/api"
	operationv1 "github.com/mindevis/qstack/gen/go/qstack/operation/v1"
)

// Spec 03 §2: every public RPC declares exactly one permission.
func TestOperationServiceHasPermissions(t *testing.T) {
	svc := operationv1.File_qstack_operation_v1_operation_proto.Services().ByName("OperationService")
	if svc == nil {
		t.Fatal("OperationService not found")
	}
	if problems := CheckServices(svc); len(problems) > 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}

	got, ok := Permission(svc.Methods().ByName("GetOperation"))
	if !ok || got != "operations.operations.get" {
		t.Errorf("Permission(GetOperation) = %q, %v; want operations.operations.get, true", got, ok)
	}
}

func TestCheckServicesReportsProblems(t *testing.T) {
	svc := buildService(t, map[string]string{
		"NoPermission":  "",
		"BadPermission": "compute.create",
		"GoodOne":       "compute.instances.create",
		"CamelCaseVerb": "iam.serviceAccounts.actAs",
	})

	got := CheckServices(svc)
	want := []string{
		`test.v1.TestService.NoPermission: missing (qstack.api.permission)`,
		`test.v1.TestService.BadPermission: permission "compute.create" is not <service>.<resource>.<verb>`,
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("CheckServices() =\n%v\nwant\n%v", got, want)
	}
}

func TestIsSensitive(t *testing.T) {
	op := (&operationv1.Operation{}).ProtoReflect().Descriptor()
	if IsSensitive(op.Fields().ByName("id")) {
		t.Error("Operation.id must not be sensitive")
	}

	fd := buildSensitiveField(t)
	if !IsSensitive(fd) {
		t.Error("field marked (qstack.api.sensitive) = true must be sensitive")
	}
}

// buildService creates a service whose methods carry the given permissions
// ("" means no option).
func buildService(t *testing.T, perms map[string]string) protoreflect.ServiceDescriptor {
	t.Helper()
	empty := "google.protobuf.Empty"
	svc := &descriptorpb.ServiceDescriptorProto{Name: proto.String("TestService")}
	names := make([]string, 0, len(perms))
	for n := range perms {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, name := range names {
		opts := &descriptorpb.MethodOptions{}
		if p := perms[name]; p != "" {
			proto.SetExtension(opts, apipb.E_Permission, p)
		}
		svc.Method = append(svc.Method, &descriptorpb.MethodDescriptorProto{
			Name:       proto.String(name),
			InputType:  proto.String("." + empty),
			OutputType: proto.String("." + empty),
			Options:    opts,
		})
	}
	file := newFile(t, &descriptorpb.FileDescriptorProto{
		Name:       proto.String("test/v1/test.proto"),
		Package:    proto.String("test.v1"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/empty.proto"},
		Service:    []*descriptorpb.ServiceDescriptorProto{svc},
	})
	return file.Services().Get(0)
}

func buildSensitiveField(t *testing.T) protoreflect.FieldDescriptor {
	t.Helper()
	opts := &descriptorpb.FieldOptions{}
	proto.SetExtension(opts, apipb.E_Sensitive, true)
	file := newFile(t, &descriptorpb.FileDescriptorProto{
		Name:    proto.String("test/v1/secret.proto"),
		Package: proto.String("test.v1"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("Secret"),
			Field: []*descriptorpb.FieldDescriptorProto{{
				Name:     proto.String("password"),
				JsonName: proto.String("password"),
				Number:   proto.Int32(1),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				Options:  opts,
			}},
		}},
	})
	return file.Messages().Get(0).Fields().Get(0)
}

func newFile(t *testing.T, fdp *descriptorpb.FileDescriptorProto) protoreflect.FileDescriptor {
	t.Helper()
	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("protodesc.NewFile: %v", err)
	}
	return fd
}
