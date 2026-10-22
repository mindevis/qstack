package apierr

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// RequestIDHeader carries the request ID in gRPC metadata and HTTP headers (REQ-PLT-061).
const RequestIDHeader = "x-request-id"

type requestIDKey struct{}

// WithRequestID returns ctx carrying the request ID.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID returns the request ID of ctx, or "" if none.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// Options configures the server interceptors.
type Options struct {
	// OnInternal is called with the original cause of every error that is
	// replaced by a generic INTERNAL error (including recovered panics), so it
	// can be logged with the request ID. May be nil.
	OnInternal func(ctx context.Context, cause error)
}

// UnaryServerInterceptor assigns a request ID, recovers panics and sanitizes errors.
func UnaryServerInterceptor(opts Options) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		ctx, id := withIncomingRequestID(ctx)
		_ = grpc.SetHeader(ctx, metadata.Pairs(RequestIDHeader, id))

		defer func() {
			if r := recover(); r != nil {
				resp, err = nil, panicError(r)
			}
			err = sanitize(ctx, err, opts)
		}()
		return handler(ctx, req)
	}
}

// StreamServerInterceptor is the streaming counterpart of UnaryServerInterceptor.
func StreamServerInterceptor(opts Options) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		ctx, id := withIncomingRequestID(ss.Context())
		_ = ss.SetHeader(metadata.Pairs(RequestIDHeader, id))

		defer func() {
			if r := recover(); r != nil {
				err = panicError(r)
			}
			err = sanitize(ctx, err, opts)
		}()
		return handler(srv, &contextStream{ServerStream: ss, ctx: ctx})
	}
}

type contextStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *contextStream) Context() context.Context { return s.ctx }

// withIncomingRequestID reuses a well-formed client request ID or generates one.
func withIncomingRequestID(ctx context.Context) (context.Context, string) {
	var id string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get(RequestIDHeader); len(v) > 0 && validRequestID(v[0]) {
			id = v[0]
		}
	}
	if id == "" {
		id = newRequestID()
	}
	return WithRequestID(ctx, id), id
}

// validRequestID accepts 1-64 characters of [A-Za-z0-9._-] so that client
// input can be logged and echoed safely.
func validRequestID(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
		if !ok {
			return false
		}
	}
	return true
}

func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails (Go >= 1.24)
	return hex.EncodeToString(b[:])
}

type panicErr struct{ value any }

func (p panicErr) Error() string { return fmt.Sprintf("panic: %v", p.value) }

func panicError(r any) error { return panicErr{value: r} }

// sanitize keeps qStack status errors as they are and turns everything that
// could leak internals (plain Go errors, panics, UNKNOWN/INTERNAL/DATA_LOSS)
// into a generic INTERNAL error with the request ID.
func sanitize(ctx context.Context, err error, opts Options) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.Unknown, codes.Internal, codes.DataLoss:
		default:
			return err
		}
	}
	if opts.OnInternal != nil {
		opts.OnInternal(ctx, err)
	}
	st, detErr := status.New(codes.Internal, internalMessage).
		WithDetails(&errdetails.RequestInfo{RequestId: RequestID(ctx)})
	if detErr != nil {
		return Internal()
	}
	return st.Err()
}
