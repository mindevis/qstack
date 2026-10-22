// Package idempotency makes mutating API calls safe to retry (spec 02 §5.4,
// REQ-PLT-050).
//
// A client sends the gRPC metadata x-idempotency-key (HTTP header
// Idempotency-Key). Within 24 hours a repeat with the same subject, key and
// request body returns the Operation of the first call; the same key with a
// different body fails with INVALID_ARGUMENT / QS_IDEMPOTENCY_KEY_REUSED.
//
// The interceptor only looks keys up. The key is recorded by
// operation.Store.Create in the same transaction as the operation and the
// resource change, and a unique index lets exactly one of two concurrent
// identical calls win: the loser's Create returns ErrDuplicateRequest, its
// transaction rolls back, and the interceptor answers with the winner's
// Operation.
package idempotency

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	operationv1 "github.com/mindevis/qstack/gen/go/qstack/operation/v1"
	"github.com/mindevis/qstack/pkg/apierr"
)

const (
	// MetadataKey is the gRPC metadata carrying the key.
	MetadataKey = "x-idempotency-key"
	// HTTPHeader is the REST header mapped to MetadataKey by the gateway.
	HTTPHeader = "Idempotency-Key"
	// MaxKeyLength bounds the key; a UUID fits.
	MaxKeyLength = 64
	// TTL is how long a key is remembered.
	TTL = 24 * time.Hour
)

var validKey = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// ErrDuplicateRequest is returned by operation.Store.Create when another
// request already recorded the same subject and key. Handlers must return it
// (possibly wrapped) unchanged so the interceptor can answer with the
// winner's Operation.
var ErrDuplicateRequest = errors.New("idempotency: duplicate request")

// Reservation is what a request may record: the interceptor puts it into the
// context of a mutating call that carries a fresh key.
type Reservation struct {
	Subject     string
	Key         string
	RequestHash []byte
}

type reservationKey struct{}

// WithReservation returns ctx carrying r.
func WithReservation(ctx context.Context, r Reservation) context.Context {
	return context.WithValue(ctx, reservationKey{}, r)
}

// ReservationFrom returns the reservation of the current request, if any.
func ReservationFrom(ctx context.Context) (Reservation, bool) {
	r, ok := ctx.Value(reservationKey{}).(Reservation)
	return r, ok
}

// Record is a remembered key.
type Record struct {
	RequestHash []byte
	Operation   *operationv1.Operation
}

// Store looks up remembered keys; operation.Store implements it.
type Store interface {
	// Lookup returns the unexpired record for subject and key.
	Lookup(ctx context.Context, subject, key string) (Record, bool, error)
}

// Options configures the interceptor.
type Options struct {
	Store Store
	// Subject returns the authenticated caller's subject ID, "" if none.
	// Keys are scoped per subject so callers never see each other's
	// operations.
	Subject func(ctx context.Context) string
}

// UnaryServerInterceptor implements REQ-PLT-050 for mutating methods (see
// IsMutating). Place it after authentication and inside the apierr
// interceptor, so that it sees handler errors unsanitized.
func UnaryServerInterceptor(opts Options) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		msg, ok := req.(proto.Message)
		if !ok || !IsMutating(info.FullMethod) {
			return handler(ctx, req)
		}
		key, present, err := keyFrom(ctx)
		if err != nil {
			return nil, err
		}
		if !present {
			return handler(ctx, req)
		}
		subject := opts.Subject(ctx)
		if subject == "" {
			return nil, apierr.Unauthenticated()
		}
		hash, err := RequestHash(info.FullMethod, msg)
		if err != nil {
			return nil, err
		}

		if op, found, err := replay(ctx, opts.Store, subject, key, hash); found || err != nil {
			return op, err
		}
		resp, err := handler(WithReservation(ctx, Reservation{Subject: subject, Key: key, RequestHash: hash}), req)
		if !errors.Is(err, ErrDuplicateRequest) {
			return resp, err
		}
		// A concurrent identical call won the race; answer with its result.
		op, found, err := replay(ctx, opts.Store, subject, key, hash)
		if err == nil && !found {
			err = apierr.Unavailable("concurrent request with the same idempotency key; retry")
		}
		return op, err
	}
}

// replay returns the remembered Operation for the key, or
// QS_IDEMPOTENCY_KEY_REUSED if it was recorded for a different body.
func replay(ctx context.Context, s Store, subject, key string, hash []byte) (*operationv1.Operation, bool, error) {
	rec, found, err := s.Lookup(ctx, subject, key)
	if err != nil {
		return nil, false, fmt.Errorf("idempotency: lookup: %w", err)
	}
	if !found {
		return nil, false, nil
	}
	if string(rec.RequestHash) != string(hash) {
		return nil, true, apierr.IdempotencyKeyReused()
	}
	return rec.Operation, true, nil
}

func keyFrom(ctx context.Context) (string, bool, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	vals := md.Get(MetadataKey)
	switch {
	case len(vals) == 0:
		return "", false, nil
	case len(vals) > 1 || !validKey.MatchString(vals[0]):
		return "", false, apierr.InvalidFields(apierr.FieldViolation{
			Field:       HTTPHeader,
			Description: fmt.Sprintf("must be a single value of 1-%d characters [A-Za-z0-9._:-], e.g. a UUID", MaxKeyLength),
		})
	}
	return vals[0], true, nil
}

// RequestHash identifies a request body: SHA-256 over the method name and
// the deterministic protobuf encoding of the request.
func RequestHash(fullMethod string, req proto.Message) ([]byte, error) {
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("idempotency: marshal request: %w", err)
	}
	h := sha256.New()
	h.Write([]byte(fullMethod))
	h.Write([]byte{0})
	h.Write(b)
	return h.Sum(nil), nil
}

var operationName = (&operationv1.Operation{}).ProtoReflect().Descriptor().FullName()

var mutating sync.Map // full method → bool

// IsMutating reports whether fullMethod ("/pkg.Service/Method") is a
// mutating method: unary, returns qstack.operation.v1.Operation (spec 02
// §5.3) and is not mapped to HTTP GET. Unknown methods are not mutating.
func IsMutating(fullMethod string) bool {
	if v, ok := mutating.Load(fullMethod); ok {
		return v.(bool)
	}
	v := isMutating(fullMethod)
	mutating.Store(fullMethod, v)
	return v
}

func isMutating(fullMethod string) bool {
	svc, method, ok := strings.Cut(strings.TrimPrefix(fullMethod, "/"), "/")
	if !ok {
		return false
	}
	d, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(svc))
	if err != nil {
		return false
	}
	sd, ok := d.(protoreflect.ServiceDescriptor)
	if !ok {
		return false
	}
	md := sd.Methods().ByName(protoreflect.Name(method))
	if md == nil || md.IsStreamingClient() || md.IsStreamingServer() || md.Output().FullName() != operationName {
		return false
	}
	rule, _ := proto.GetExtension(md.Options(), annotations.E_Http).(*annotations.HttpRule)
	return rule.GetGet() == ""
}

// IncomingHeaderMatcher maps the Idempotency-Key HTTP header to gRPC
// metadata; for grpc-gateway's runtime.WithIncomingHeaderMatcher, chained
// before the default matcher.
func IncomingHeaderMatcher(header string) (string, bool) {
	if strings.EqualFold(header, HTTPHeader) {
		return MetadataKey, true
	}
	return "", false
}
