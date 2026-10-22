// Package apierr builds API errors in the qStack format (spec 02 §5.5): a gRPC
// status code plus google.rpc.ErrorInfo{reason: QS_*, domain: "qstack.cloud"}
// and, where relevant, BadRequest / QuotaFailure / RetryInfo details.
//
// Messages are English and must not contain internal details such as stack
// traces, SQL or host names (REQ-PLT-060). Unexpected errors are turned into a
// generic INTERNAL error by the server interceptor (see interceptor.go).
package apierr

import (
	"fmt"
	"strings"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/protoadapt"
	"google.golang.org/protobuf/types/known/durationpb"

	apipb "github.com/mindevis/qstack/gen/go/qstack/api"
)

// Domain is the ErrorInfo.domain of every qStack error.
const Domain = "qstack.cloud"

// New returns an error with the given code, reason and message. Metadata is
// copied into ErrorInfo.metadata; extra details are appended after ErrorInfo.
func New(code codes.Code, reason apipb.ErrorReason, msg string, md map[string]string, extra ...protoadapt.MessageV1) error {
	st := status.New(code, msg)
	details := append([]protoadapt.MessageV1{&errdetails.ErrorInfo{
		Reason:   reason.String(),
		Domain:   Domain,
		Metadata: md,
	}}, extra...)
	withDetails, err := st.WithDetails(details...)
	if err != nil {
		// Only happens if a detail cannot be marshalled: a programming error.
		panic(fmt.Sprintf("apierr: attach details: %v", err))
	}
	return withDetails.Err()
}

// FieldViolation describes one invalid request field.
type FieldViolation struct {
	// Field is the path, e.g. "network_interfaces[0].subnet_id".
	Field       string
	Description string
}

// InvalidFields reports request validation failures (INVALID_ARGUMENT, QS_INVALID_FIELD).
func InvalidFields(violations ...FieldViolation) error {
	br := &errdetails.BadRequest{}
	for _, v := range violations {
		br.FieldViolations = append(br.FieldViolations, &errdetails.BadRequest_FieldViolation{
			Field: v.Field, Description: v.Description,
		})
	}
	msg := "invalid request"
	switch len(violations) {
	case 0:
	case 1:
		msg = fmt.Sprintf("invalid request: %s: %s", violations[0].Field, violations[0].Description)
	default:
		msg = fmt.Sprintf("invalid request: %d invalid fields", len(violations))
	}
	return New(codes.InvalidArgument, apipb.ErrorReason_QS_INVALID_FIELD, msg, nil, br)
}

// IdempotencyKeyReused reports a reused idempotency key with a different body.
func IdempotencyKeyReused() error {
	return New(codes.InvalidArgument, apipb.ErrorReason_QS_IDEMPOTENCY_KEY_REUSED,
		"idempotency key was already used with a different request", nil)
}

// NotFound reports a missing resource. Also used when the caller may not see
// the resource, so that its existence is not revealed.
func NotFound(resourceType, id string) error {
	return New(codes.NotFound, apipb.ErrorReason_QS_NOT_FOUND,
		fmt.Sprintf("%s %s not found", resourceType, id),
		map[string]string{"resource_type": resourceType, "resource_id": id})
}

// PermissionDenied reports a missing permission on a resource the caller can see.
func PermissionDenied(permission string) error {
	return New(codes.PermissionDenied, apipb.ErrorReason_QS_PERMISSION_DENIED,
		"permission denied: "+permission, map[string]string{"permission": permission})
}

// Unauthenticated reports a request without credentials.
func Unauthenticated() error {
	return New(codes.Unauthenticated, apipb.ErrorReason_QS_UNAUTHENTICATED, "authentication required", nil)
}

// TokenExpired reports an expired or revoked token.
func TokenExpired() error {
	return New(codes.Unauthenticated, apipb.ErrorReason_QS_TOKEN_EXPIRED, "token expired or revoked", nil)
}

// NameConflict reports a live resource with the same name in the project.
func NameConflict(resourceType, name string) error {
	return New(codes.AlreadyExists, apipb.ErrorReason_QS_NAME_CONFLICT,
		fmt.Sprintf("%s named %q already exists", resourceType, name),
		map[string]string{"resource_type": resourceType, "name": name})
}

// ResourceBusy reports another running mutating operation on the resource.
func ResourceBusy(id, operationID string) error {
	return New(codes.FailedPrecondition, apipb.ErrorReason_QS_RESOURCE_BUSY,
		fmt.Sprintf("%s is busy: operation %s is in progress", id, operationID),
		map[string]string{"resource_id": id, "operation_id": operationID})
}

// InvalidState reports that the resource state does not allow the action.
func InvalidState(id, state, action string) error {
	return New(codes.FailedPrecondition, apipb.ErrorReason_QS_INVALID_STATE,
		fmt.Sprintf("cannot %s %s in state %s", action, id, state),
		map[string]string{"resource_id": id, "state": state})
}

// ResourceInUse reports that other resources depend on the resource.
func ResourceInUse(id string, usedBy ...string) error {
	return New(codes.FailedPrecondition, apipb.ErrorReason_QS_RESOURCE_IN_USE,
		fmt.Sprintf("%s is in use by %s", id, strings.Join(usedBy, ", ")),
		map[string]string{"resource_id": id, "used_by": strings.Join(usedBy, ",")})
}

// QuotaViolation describes one exceeded quota metric.
type QuotaViolation struct {
	Metric    string // e.g. "compute.instances.count"
	Limit     int64
	Used      int64
	Requested int64
}

// QuotaExceeded reports exceeded project quotas (RESOURCE_EXHAUSTED + QuotaFailure).
func QuotaExceeded(violations ...QuotaViolation) error {
	qf := &errdetails.QuotaFailure{}
	metrics := make([]string, 0, len(violations))
	for _, v := range violations {
		qf.Violations = append(qf.Violations, &errdetails.QuotaFailure_Violation{
			Subject:     v.Metric,
			Description: fmt.Sprintf("limit %d, used %d, requested %d", v.Limit, v.Used, v.Requested),
		})
		metrics = append(metrics, v.Metric)
	}
	return New(codes.ResourceExhausted, apipb.ErrorReason_QS_QUOTA_EXCEEDED,
		"quota exceeded: "+strings.Join(metrics, ", "), nil, qf)
}

// ZoneCapacity reports that the zone cannot fit the request.
func ZoneCapacity(zone string) error {
	return New(codes.ResourceExhausted, apipb.ErrorReason_QS_ZONE_CAPACITY,
		"not enough capacity in zone "+zone, map[string]string{"zone": zone})
}

// RateLimited reports an exceeded API rate limit; retryAfter becomes RetryInfo
// (and the Retry-After HTTP header at the gateway).
func RateLimited(retryAfter time.Duration) error {
	return New(codes.ResourceExhausted, apipb.ErrorReason_QS_RATE_LIMITED, "rate limit exceeded", nil,
		&errdetails.RetryInfo{RetryDelay: durationpb.New(retryAfter)})
}

// EtagMismatch reports an optimistic concurrency conflict (ABORTED).
func EtagMismatch(id string) error {
	return New(codes.Aborted, apipb.ErrorReason_QS_ETAG_MISMATCH,
		fmt.Sprintf("%s was modified concurrently; reload it and retry", id),
		map[string]string{"resource_id": id})
}

// LocalDiskNodeMismatch reports an attempt to attach a local disk to an
// instance on another node.
func LocalDiskNodeMismatch(diskID, instanceID string) error {
	return New(codes.FailedPrecondition, apipb.ErrorReason_QS_LOCAL_DISK_NODE_MISMATCH,
		fmt.Sprintf("local disk %s can only be attached to instances on its node, %s is on another node", diskID, instanceID),
		map[string]string{"disk_id": diskID, "instance_id": instanceID})
}

// ServiceNotAvailable reports a service disabled in this installation.
func ServiceNotAvailable(service string) error {
	return New(codes.FailedPrecondition, apipb.ErrorReason_QS_SERVICE_NOT_AVAILABLE,
		service+" is not available in this installation", map[string]string{"service": service})
}

// BillingCurrencyLocked reports a currency change after the first invoice.
func BillingCurrencyLocked() error {
	return New(codes.FailedPrecondition, apipb.ErrorReason_QS_BILLING_CURRENCY_LOCKED,
		"billing country and currency cannot change after the first invoice", nil)
}

// Unavailable reports a temporary failure; clients may retry.
func Unavailable(msg string) error {
	return status.Error(codes.Unavailable, msg)
}

// internalMessage is the only message an INTERNAL error ever carries.
const internalMessage = "internal error"

// Internal returns a generic INTERNAL error. Use it for unexpected failures;
// the cause must be logged, never returned to the client.
func Internal() error {
	return status.Error(codes.Internal, internalMessage)
}

// Reason returns the QS_* reason of err, or ERROR_REASON_UNSPECIFIED.
func Reason(err error) apipb.ErrorReason {
	st, ok := status.FromError(err)
	if !ok {
		return apipb.ErrorReason_ERROR_REASON_UNSPECIFIED
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetDomain() == Domain {
			if v, ok := apipb.ErrorReason_value[info.GetReason()]; ok {
				return apipb.ErrorReason(v)
			}
		}
	}
	return apipb.ErrorReason_ERROR_REASON_UNSPECIFIED
}

// IsReason reports whether err carries the given reason.
func IsReason(err error, reason apipb.ErrorReason) bool {
	return err != nil && Reason(err) == reason
}
