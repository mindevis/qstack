package audit

import (
	"log/slog"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/mindevis/qstack/pkg/protoopts"
)

// Redact returns a copy of m without (qstack.api.sensitive) fields, at any
// depth: nested messages, lists, maps and google.protobuf.Any payloads. An
// Any whose type is not linked into the binary is dropped, since its fields
// cannot be inspected (REQ-SEC-005).
func Redact(m proto.Message) proto.Message {
	if m == nil {
		return nil
	}
	c := proto.Clone(m)
	redact(c.ProtoReflect())
	return c
}

func redact(m protoreflect.Message) {
	// By descriptor, not Go type: dynamic messages hold Any as dynamicpb.
	if m.Descriptor().FullName() == anyName {
		redactAny(m)
		return
	}
	// The message must not change while Range iterates; clear afterwards.
	var sensitive []protoreflect.FieldDescriptor
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if protoopts.IsSensitive(fd) {
			sensitive = append(sensitive, fd)
			return true
		}
		switch {
		case fd.IsMap() && fd.MapValue().Message() != nil:
			v.Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
				redact(mv.Message())
				return true
			})
		case fd.IsList() && fd.Message() != nil:
			for i := range v.List().Len() {
				redact(v.List().Get(i).Message())
			}
		case fd.Message() != nil && !fd.IsList() && !fd.IsMap():
			redact(v.Message())
		}
		return true
	})
	for _, fd := range sensitive {
		m.Clear(fd)
	}
}

var anyName = (&anypb.Any{}).ProtoReflect().Descriptor().FullName()

// redactAny redacts the payload of an Any in place, or empties the Any if
// the payload type is unknown or cannot be decoded.
func redactAny(m protoreflect.Message) {
	fields := m.Descriptor().Fields()
	typeURL, value := fields.ByName("type_url"), fields.ByName("value")
	a := &anypb.Any{TypeUrl: m.Get(typeURL).String(), Value: m.Get(value).Bytes()}
	inner, err := a.UnmarshalNew()
	if err == nil {
		redact(inner.ProtoReflect())
		err = a.MarshalFrom(inner)
	}
	if err != nil {
		m.Clear(typeURL)
		m.Clear(value)
		return
	}
	m.Set(value, protoreflect.ValueOfBytes(a.GetValue()))
}

// RedactedStruct converts the redacted m to a JSON object for AuditEvent.request.
func RedactedStruct(m proto.Message) *structpb.Struct {
	b, err := protojson.Marshal(Redact(m))
	if err != nil {
		return nil
	}
	s := &structpb.Struct{}
	if err := protojson.Unmarshal(b, s); err != nil {
		return nil
	}
	return s
}

// LogValue is a log attribute value for a proto message with sensitive
// fields removed: slog.Any("request", audit.LogValue(req)). Never log a
// request message directly.
func LogValue(m proto.Message) slog.Value {
	b, err := protojson.Marshal(Redact(m))
	if err != nil {
		return slog.StringValue("<unprintable>")
	}
	return slog.StringValue(string(b))
}
