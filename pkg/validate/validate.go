// Package validate checks requests against their protovalidate rules
// (spec 02 §1) before the handler runs: a violation is INVALID_ARGUMENT /
// QS_INVALID_FIELD with one google.rpc.BadRequest field violation per rule.
package validate

import (
	"context"
	"errors"

	"buf.build/go/protovalidate"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/mindevis/qstack/pkg/apierr"
)

// Validator validates messages; safe for concurrent use.
type Validator struct{ v protovalidate.Validator }

// New returns a Validator.
func New() (*Validator, error) {
	v, err := protovalidate.New()
	if err != nil {
		return nil, err
	}
	return &Validator{v: v}, nil
}

// Validate returns nil or an INVALID_ARGUMENT error describing every
// violated rule of msg.
func (v *Validator) Validate(msg proto.Message) error {
	err := v.v.Validate(msg)
	if err == nil {
		return nil
	}
	var verr *protovalidate.ValidationError
	if !errors.As(err, &verr) {
		// A rule failed to compile or evaluate: a bug in the proto, not
		// in the request.
		return err
	}
	fvs := make([]apierr.FieldViolation, 0, len(verr.Violations))
	for _, vi := range verr.Violations {
		fvs = append(fvs, apierr.FieldViolation{
			Field:       protovalidate.FieldPathString(vi.Proto.GetField()),
			Description: vi.Proto.GetMessage(),
		})
	}
	return apierr.InvalidFields(fvs...)
}

// UnaryServerInterceptor validates every request message. Install it inside
// pkg/audit, so that invalid calls are audited, and outside pkg/idempotency.
func (v *Validator) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if msg, ok := req.(proto.Message); ok {
			if err := v.Validate(msg); err != nil {
				return nil, err
			}
		}
		return handler(ctx, req)
	}
}
