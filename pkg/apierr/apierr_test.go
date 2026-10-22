package apierr

import (
	"maps"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apipb "github.com/mindevis/qstack/gen/go/qstack/api"
)

// Spec 02 §5.5: code + ErrorInfo{reason, domain} for every constructor.
func TestConstructors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		code   codes.Code
		reason apipb.ErrorReason
		md     map[string]string
		detail string // expected extra detail type, if any
	}{
		{"invalid fields", InvalidFields(FieldViolation{"name", "must match ^[a-z]..."}), codes.InvalidArgument, apipb.ErrorReason_QS_INVALID_FIELD, nil, "BadRequest"},
		{"idempotency", IdempotencyKeyReused(), codes.InvalidArgument, apipb.ErrorReason_QS_IDEMPOTENCY_KEY_REUSED, nil, ""},
		{"not found", NotFound("instance", "ins-x"), codes.NotFound, apipb.ErrorReason_QS_NOT_FOUND, map[string]string{"resource_type": "instance", "resource_id": "ins-x"}, ""},
		{"permission", PermissionDenied("compute.instances.create"), codes.PermissionDenied, apipb.ErrorReason_QS_PERMISSION_DENIED, map[string]string{"permission": "compute.instances.create"}, ""},
		{"unauthenticated", Unauthenticated(), codes.Unauthenticated, apipb.ErrorReason_QS_UNAUTHENTICATED, nil, ""},
		{"token expired", TokenExpired(), codes.Unauthenticated, apipb.ErrorReason_QS_TOKEN_EXPIRED, nil, ""},
		{"name conflict", NameConflict("disk", "data"), codes.AlreadyExists, apipb.ErrorReason_QS_NAME_CONFLICT, map[string]string{"resource_type": "disk", "name": "data"}, ""},
		{"busy", ResourceBusy("ins-x", "op-y"), codes.FailedPrecondition, apipb.ErrorReason_QS_RESOURCE_BUSY, map[string]string{"resource_id": "ins-x", "operation_id": "op-y"}, ""},
		{"invalid state", InvalidState("ins-x", "RUNNING", "resize"), codes.FailedPrecondition, apipb.ErrorReason_QS_INVALID_STATE, map[string]string{"resource_id": "ins-x", "state": "RUNNING"}, ""},
		{"in use", ResourceInUse("sub-x", "nic-a", "nic-b"), codes.FailedPrecondition, apipb.ErrorReason_QS_RESOURCE_IN_USE, map[string]string{"resource_id": "sub-x", "used_by": "nic-a,nic-b"}, ""},
		{"quota", QuotaExceeded(QuotaViolation{"compute.instances.count", 10, 10, 1}), codes.ResourceExhausted, apipb.ErrorReason_QS_QUOTA_EXCEEDED, nil, "QuotaFailure"},
		{"zone capacity", ZoneCapacity("ru-central1-a"), codes.ResourceExhausted, apipb.ErrorReason_QS_ZONE_CAPACITY, map[string]string{"zone": "ru-central1-a"}, ""},
		{"rate limited", RateLimited(2 * time.Second), codes.ResourceExhausted, apipb.ErrorReason_QS_RATE_LIMITED, nil, "RetryInfo"},
		{"etag", EtagMismatch("ins-x"), codes.Aborted, apipb.ErrorReason_QS_ETAG_MISMATCH, map[string]string{"resource_id": "ins-x"}, ""},
		{"local disk", LocalDiskNodeMismatch("dsk-x", "ins-y"), codes.FailedPrecondition, apipb.ErrorReason_QS_LOCAL_DISK_NODE_MISMATCH, map[string]string{"disk_id": "dsk-x", "instance_id": "ins-y"}, ""},
		{"service n/a", ServiceNotAvailable("objectstore"), codes.FailedPrecondition, apipb.ErrorReason_QS_SERVICE_NOT_AVAILABLE, map[string]string{"service": "objectstore"}, ""},
		{"currency locked", BillingCurrencyLocked(), codes.FailedPrecondition, apipb.ErrorReason_QS_BILLING_CURRENCY_LOCKED, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := status.Convert(tt.err)
			if st.Code() != tt.code {
				t.Errorf("code = %v, want %v", st.Code(), tt.code)
			}
			if st.Message() == "" {
				t.Error("empty message")
			}
			info, extra := splitDetails(t, st)
			if info == nil {
				t.Fatal("no ErrorInfo detail")
			}
			if info.GetDomain() != Domain || info.GetReason() != tt.reason.String() {
				t.Errorf("ErrorInfo = {%s %s}, want {%s %s}", info.GetDomain(), info.GetReason(), Domain, tt.reason)
			}
			if tt.md != nil && !maps.Equal(info.GetMetadata(), tt.md) {
				t.Errorf("metadata = %v, want %v", info.GetMetadata(), tt.md)
			}
			if tt.detail != extra {
				t.Errorf("extra detail = %q, want %q", extra, tt.detail)
			}
			if got := Reason(tt.err); got != tt.reason {
				t.Errorf("Reason() = %v, want %v", got, tt.reason)
			}
			if !IsReason(tt.err, tt.reason) {
				t.Error("IsReason() = false")
			}
		})
	}
}

func TestReasonOfForeignErrors(t *testing.T) {
	for _, err := range []error{nil, Internal(), Unavailable("try later"), status.Error(codes.NotFound, "x")} {
		if got := Reason(err); got != apipb.ErrorReason_ERROR_REASON_UNSPECIFIED {
			t.Errorf("Reason(%v) = %v, want UNSPECIFIED", err, got)
		}
	}
	if IsReason(nil, apipb.ErrorReason_ERROR_REASON_UNSPECIFIED) {
		t.Error("IsReason(nil, ...) = true")
	}
}

func TestQuotaFailureDetails(t *testing.T) {
	st := status.Convert(QuotaExceeded(
		QuotaViolation{"compute.instances.count", 10, 10, 1},
		QuotaViolation{"compute.vcpus.total", 64, 62, 4},
	))
	for _, d := range st.Details() {
		if qf, ok := d.(*errdetails.QuotaFailure); ok {
			if len(qf.GetViolations()) != 2 || qf.GetViolations()[1].GetSubject() != "compute.vcpus.total" {
				t.Errorf("violations = %v", qf.GetViolations())
			}
			return
		}
	}
	t.Fatal("no QuotaFailure detail")
}

// splitDetails returns the ErrorInfo and the type name of the other detail.
func splitDetails(t *testing.T, st *status.Status) (*errdetails.ErrorInfo, string) {
	t.Helper()
	var (
		info  *errdetails.ErrorInfo
		extra string
	)
	for _, d := range st.Details() {
		switch v := d.(type) {
		case *errdetails.ErrorInfo:
			info = v
		case *errdetails.BadRequest:
			extra = "BadRequest"
		case *errdetails.QuotaFailure:
			extra = "QuotaFailure"
		case *errdetails.RetryInfo:
			extra = "RetryInfo"
		case error:
			t.Fatalf("undecodable detail: %v", v)
		}
	}
	return info, extra
}
