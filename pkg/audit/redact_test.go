package audit

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/anypb"

	apipb "github.com/mindevis/qstack/gen/go/qstack/api"
)

// testTypes builds and registers once:
//
//	message Secret  { string password = 1 [sensitive]; string name = 2; }
//	message Request { string name = 1; string token = 2 [sensitive];
//	                  Secret nested = 3; repeated Secret list = 4;
//	                  map<string, Secret> by_name = 5; google.protobuf.Any any = 6;
//	                  Secret hidden = 7 [sensitive]; }
var testTypes = sync.OnceValues(func() (secret, request protoreflect.MessageType) {
	sens := &descriptorpb.FieldOptions{}
	proto.SetExtension(sens, apipb.E_Sensitive, true)
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()
	msg := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum()
	opt := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
	rep := descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	field := func(name string, n int32, label *descriptorpb.FieldDescriptorProto_Label, typ *descriptorpb.FieldDescriptorProto_Type, typeName string, o *descriptorpb.FieldOptions) *descriptorpb.FieldDescriptorProto {
		f := &descriptorpb.FieldDescriptorProto{Name: proto.String(name), JsonName: proto.String(name), Number: proto.Int32(n), Label: label, Type: typ, Options: o}
		if typeName != "" {
			f.TypeName = proto.String(typeName)
		}
		return f
	}
	fdp := &descriptorpb.FileDescriptorProto{
		Name: proto.String("audittest/v1/redact.proto"), Package: proto.String("audittest.v1"), Syntax: proto.String("proto3"),
		Dependency: []string{"google/protobuf/any.proto", "qstack/api/annotations.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("Secret"), Field: []*descriptorpb.FieldDescriptorProto{
				field("password", 1, opt, str, "", sens), field("name", 2, opt, str, "", nil)}},
			{Name: proto.String("Request"), Field: []*descriptorpb.FieldDescriptorProto{
				field("name", 1, opt, str, "", nil),
				field("token", 2, opt, str, "", sens),
				field("nested", 3, opt, msg, ".audittest.v1.Secret", nil),
				field("list", 4, rep, msg, ".audittest.v1.Secret", nil),
				field("by_name", 5, rep, msg, ".audittest.v1.Request.ByNameEntry", nil),
				field("any", 6, opt, msg, ".google.protobuf.Any", nil),
				field("hidden", 7, opt, msg, ".audittest.v1.Secret", sens),
			}, NestedType: []*descriptorpb.DescriptorProto{{
				Name:    proto.String("ByNameEntry"),
				Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
				Field: []*descriptorpb.FieldDescriptorProto{
					field("key", 1, opt, str, "", nil), field("value", 2, opt, msg, ".audittest.v1.Secret", nil)},
			}}},
		},
	}
	fd, err := protodesc.NewFile(fdp, protoregistry.GlobalFiles)
	if err != nil {
		panic(err)
	}
	if err := protoregistry.GlobalFiles.RegisterFile(fd); err != nil {
		panic(err)
	}
	secret = dynamicpb.NewMessageType(fd.Messages().ByName("Secret"))
	request = dynamicpb.NewMessageType(fd.Messages().ByName("Request"))
	for _, mt := range []protoreflect.MessageType{secret, request} {
		if err := protoregistry.GlobalTypes.RegisterMessage(mt); err != nil {
			panic(err)
		}
	}
	return secret, request
})

func newSecret(t *testing.T, password, name string) protoreflect.Message {
	t.Helper()
	secretT, _ := testTypes()
	s := secretT.New()
	s.Set(s.Descriptor().Fields().ByName("password"), protoreflect.ValueOfString(password))
	s.Set(s.Descriptor().Fields().ByName("name"), protoreflect.ValueOfString(name))
	return s
}

// sampleRequest has a secret in every kind of position.
func sampleRequest(t *testing.T) proto.Message {
	t.Helper()
	_, requestT := testTypes()
	r := requestT.New()
	f := r.Descriptor().Fields()
	r.Set(f.ByName("name"), protoreflect.ValueOfString("visible-name"))
	r.Set(f.ByName("token"), protoreflect.ValueOfString("s3cr3t-token"))
	r.Set(f.ByName("nested"), protoreflect.ValueOfMessage(newSecret(t, "s3cr3t-nested", "nested-name")))
	l := r.Mutable(f.ByName("list")).List()
	l.Append(protoreflect.ValueOfMessage(newSecret(t, "s3cr3t-list", "list-name")))
	m := r.Mutable(f.ByName("by_name")).Map()
	m.Set(protoreflect.ValueOfString("k").MapKey(), protoreflect.ValueOfMessage(newSecret(t, "s3cr3t-map", "map-name")))
	a, err := anypb.New(newSecret(t, "s3cr3t-any", "any-name").Interface())
	if err != nil {
		t.Fatal(err)
	}
	r.Set(f.ByName("any"), protoreflect.ValueOfMessage(a.ProtoReflect()))
	r.Set(f.ByName("hidden"), protoreflect.ValueOfMessage(newSecret(t, "s3cr3t-hidden", "hidden-name")))
	return r.Interface()
}

// REQ-SEC-005, roadmap T-0.15 acceptance: no sensitive value reaches the
// audit request or the log, at any depth; other fields stay.
func TestSensitiveFieldsNeverLeak(t *testing.T) {
	req := sampleRequest(t)
	before, _ := protojson.Marshal(req)
	if !strings.Contains(string(before), "s3cr3t") {
		t.Fatal("sample has no secrets")
	}

	auditJSON, err := protojson.Marshal(RedactedStruct(req))
	if err != nil {
		t.Fatal(err)
	}
	var logBuf bytes.Buffer
	slog.New(slog.NewJSONHandler(&logBuf, nil)).Info("call", "request", LogValue(req))

	for name, out := range map[string]string{"audit": string(auditJSON), "log": logBuf.String()} {
		if strings.Contains(out, "s3cr3t") {
			t.Errorf("%s output leaks a secret: %s", name, out)
		}
		for _, keep := range []string{"visible-name", "nested-name", "list-name", "map-name", "any-name"} {
			if !strings.Contains(out, keep) {
				t.Errorf("%s output lost %q: %s", name, keep, out)
			}
		}
		if strings.Contains(out, "hidden-name") {
			t.Errorf("%s output kept a sensitive message: %s", name, out)
		}
	}
	after, _ := protojson.Marshal(req)
	if string(after) != string(before) {
		t.Error("Redact modified its input")
	}
}

// An Any payload whose type is unknown cannot be inspected and is dropped.
func TestUnknownAnyIsDropped(t *testing.T) {
	_, requestT := testTypes()
	r := requestT.New()
	r.Set(r.Descriptor().Fields().ByName("any"), protoreflect.ValueOfMessage(
		(&anypb.Any{TypeUrl: "type.googleapis.com/unknown.v1.Thing", Value: []byte("\x0a\x06s3cr3t")}).ProtoReflect()))
	out, _ := protojson.Marshal(Redact(r.Interface()))
	if strings.Contains(string(out), "unknown.v1.Thing") || bytes.Contains(out, []byte("s3cr3t")) {
		t.Errorf("unknown Any kept: %s", out)
	}
	if Redact(nil) != nil {
		t.Error("Redact(nil)")
	}
}
