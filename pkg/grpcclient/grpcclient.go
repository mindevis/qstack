// Package grpcclient dials internal gRPC services (spec 01 §7, 02 §11):
// discovery by DNS (headless Service), client-side round_robin over healthy
// backends (gRPC health checking), a 5 s default timeout, retries only for
// UNAVAILABLE (at most 3, with gRPC's jittered backoff), and propagation of
// the caller context: x-request-id, x-qs-subject and traceparent.
//
// Services never use the Kubernetes API for discovery.
package grpcclient

import (
	"context"
	"crypto/tls"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	_ "google.golang.org/grpc/health" // registers the client-side health checking function
	"google.golang.org/grpc/metadata"

	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/authz"
	"github.com/mindevis/qstack/pkg/telemetry"
)

// SubjectHeader carries the end user's subject to the callee. The callee
// trusts it only from mTLS-authenticated services; the user's permissions
// are checked once, by the service that received the public request.
const SubjectHeader = "x-qs-subject"

// ServiceConfig is the default service config: round_robin, health checks
// and the timeout/retry policy of spec 02 §11.
const ServiceConfig = `{
  "loadBalancingConfig": [{"round_robin": {}}],
  "healthCheckConfig": {"serviceName": ""},
  "methodConfig": [{
    "name": [{}],
    "timeout": "5s",
    "retryPolicy": {
      "maxAttempts": 4,
      "initialBackoff": "0.1s",
      "maxBackoff": "1s",
      "backoffMultiplier": 2,
      "retryableStatusCodes": ["UNAVAILABLE"]
    }
  }]
}`

// Options configures New.
type Options struct {
	// TLS is the mTLS config (service certificate + CA). Required unless
	// Insecure is set, which only tests may do.
	TLS      *tls.Config
	Insecure bool
	// Extra dial options, applied last.
	DialOptions []grpc.DialOption
}

// New returns a client connection to target, e.g.
// "dns:///qs-iam.qstack.svc.cluster.local:9090".
func New(target string, opts Options) (*grpc.ClientConn, error) {
	var creds grpc.DialOption
	switch {
	case opts.TLS != nil:
		creds = grpc.WithTransportCredentials(credentials.NewTLS(opts.TLS))
	case opts.Insecure:
		creds = grpc.WithTransportCredentials(insecure.NewCredentials())
	default:
		return nil, errors.New("grpcclient: TLS config required")
	}
	dial := []grpc.DialOption{
		creds,
		grpc.WithDefaultServiceConfig(ServiceConfig),
		grpc.WithChainUnaryInterceptor(propagateUnary),
		grpc.WithChainStreamInterceptor(propagateStream),
		telemetry.GRPCClientTracing(), // traceparent
	}
	return grpc.NewClient(target, append(dial, opts.DialOptions...)...)
}

// outgoing adds the request ID and the end user's subject to ctx.
func outgoing(ctx context.Context) context.Context {
	var kv []string
	if id := apierr.RequestID(ctx); id != "" {
		kv = append(kv, apierr.RequestIDHeader, id)
	}
	if s, ok := authz.SubjectFrom(ctx); ok {
		kv = append(kv, SubjectHeader, s.ID)
	}
	if len(kv) == 0 {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, kv...)
}

func propagateUnary(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	return invoker(outgoing(ctx), method, req, reply, cc, opts...)
}

func propagateStream(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	return streamer(outgoing(ctx), desc, cc, method, opts...)
}
