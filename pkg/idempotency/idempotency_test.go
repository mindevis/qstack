package idempotency

import (
	"context"
	"fmt"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	apipb "github.com/mindevis/qstack/gen/go/qstack/api"
	operationv1 "github.com/mindevis/qstack/gen/go/qstack/operation/v1"
	"github.com/mindevis/qstack/pkg/apierr"
)

const (
	cancelMethod = "/qstack.operation.v1.OperationService/CancelOperation" // POST, returns Operation
	getMethod    = "/qstack.operation.v1.OperationService/GetOperation"    // GET, returns Operation
	listMethod   = "/qstack.operation.v1.OperationService/ListOperations"
)

func TestIsMutating(t *testing.T) {
	for method, want := range map[string]bool{
		cancelMethod:                   true,
		getMethod:                      false,
		listMethod:                     false,
		"/qstack.operation.v1.Nope/X":  false,
		"/grpc.health.v1.Health/Check": false,
		"garbage":                      false,
		"/qstack.operation.v1.OperationService/Missing": false,
	} {
		if got := IsMutating(method); got != want {
			t.Errorf("IsMutating(%q) = %v, want %v", method, got, want)
		}
	}
}

// fakeStore is a map; put simulates a record written by operation.Store.Create.
type fakeStore struct{ recs map[string]Record }

func (s *fakeStore) Lookup(_ context.Context, subject, key string) (Record, bool, error) {
	r, ok := s.recs[subject+"/"+key]
	return r, ok, nil
}

func (s *fakeStore) put(subject, key string, hash []byte, op *operationv1.Operation) {
	s.recs[subject+"/"+key] = Record{RequestHash: hash, Operation: op}
}

type harness struct {
	store   *fakeStore
	calls   int
	icpt    grpc.UnaryServerInterceptor
	handler grpc.UnaryHandler
}

// newHarness returns an interceptor whose handler records the reservation
// like operation.Store.Create would and returns a new Operation.
func newHarness() *harness {
	h := &harness{store: &fakeStore{recs: map[string]Record{}}}
	h.icpt = UnaryServerInterceptor(Options{Store: h.store, Subject: func(context.Context) string { return "usr-1" }})
	h.handler = func(ctx context.Context, _ any) (any, error) {
		h.calls++
		op := &operationv1.Operation{Id: fmt.Sprintf("op-%d", h.calls)}
		if r, ok := ReservationFrom(ctx); ok {
			h.store.put(r.Subject, r.Key, r.RequestHash, op)
		}
		return op, nil
	}
	return h
}

func (h *harness) call(ctx context.Context, method string, req proto.Message) (*operationv1.Operation, error) {
	resp, err := h.icpt(ctx, req, &grpc.UnaryServerInfo{FullMethod: method}, h.handler)
	if err != nil {
		return nil, err
	}
	return resp.(*operationv1.Operation), nil
}

func withKey(key string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(MetadataKey, key))
}

func cancelReq(id string) *operationv1.CancelOperationRequest {
	return &operationv1.CancelOperationRequest{OperationId: id}
}

// REQ-PLT-050: same key, subject and body → same Operation, handler runs once.
func TestRepeatReturnsSameOperation(t *testing.T) {
	h := newHarness()
	ctx := withKey("6f1c2a54-0d0e-4c43-9a43-6b1f6d1c9a01")
	first, err := h.call(ctx, cancelMethod, cancelReq("op-a"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.call(ctx, cancelMethod, cancelReq("op-a"))
	if err != nil {
		t.Fatal(err)
	}
	if first.GetId() != second.GetId() || h.calls != 1 {
		t.Errorf("got %s then %s with %d handler calls, want the same operation and 1 call", first.GetId(), second.GetId(), h.calls)
	}
}

// REQ-PLT-050: same key with a different body → INVALID_ARGUMENT QS_IDEMPOTENCY_KEY_REUSED.
func TestKeyReusedWithDifferentBody(t *testing.T) {
	h := newHarness()
	ctx := withKey("k1")
	if _, err := h.call(ctx, cancelMethod, cancelReq("op-a")); err != nil {
		t.Fatal(err)
	}
	_, err := h.call(ctx, cancelMethod, cancelReq("op-b"))
	if status.Code(err) != codes.InvalidArgument || !apierr.IsReason(err, apipb.ErrorReason_QS_IDEMPOTENCY_KEY_REUSED) {
		t.Errorf("err = %v, want INVALID_ARGUMENT QS_IDEMPOTENCY_KEY_REUSED", err)
	}
}

func TestWithoutKeyOrForReadsHandlerAlwaysRuns(t *testing.T) {
	h := newHarness()
	for range 2 {
		if _, err := h.call(context.Background(), cancelMethod, cancelReq("op-a")); err != nil {
			t.Fatal(err)
		}
		if _, err := h.call(withKey("k"), getMethod, &operationv1.GetOperationRequest{OperationId: "op-a"}); err != nil {
			t.Fatal(err)
		}
	}
	if h.calls != 4 || len(h.store.recs) != 0 {
		t.Errorf("calls = %d, records = %d; want 4 calls and nothing recorded", h.calls, len(h.store.recs))
	}
}

// Keys are scoped per subject.
func TestKeysArePerSubject(t *testing.T) {
	h := newHarness()
	ctx := withKey("k")
	if _, err := h.call(ctx, cancelMethod, cancelReq("op-a")); err != nil {
		t.Fatal(err)
	}
	other := UnaryServerInterceptor(Options{Store: h.store, Subject: func(context.Context) string { return "usr-2" }})
	if _, err := other(ctx, cancelReq("op-a"), &grpc.UnaryServerInfo{FullMethod: cancelMethod}, h.handler); err != nil {
		t.Fatal(err)
	}
	if h.calls != 2 {
		t.Errorf("handler calls = %d, want 2 (another subject must not replay)", h.calls)
	}
}

func TestInvalidKeys(t *testing.T) {
	h := newHarness()
	long := make([]byte, MaxKeyLength+1)
	for i := range long {
		long[i] = 'a'
	}
	for name, ctx := range map[string]context.Context{
		"too long": withKey(string(long)),
		"empty":    withKey(""),
		"space":    withKey("a b"),
		"twice":    metadata.NewIncomingContext(context.Background(), metadata.Pairs(MetadataKey, "a", MetadataKey, "b")),
	} {
		_, err := h.call(ctx, cancelMethod, cancelReq("op-a"))
		if !apierr.IsReason(err, apipb.ErrorReason_QS_INVALID_FIELD) {
			t.Errorf("%s: err = %v, want QS_INVALID_FIELD", name, err)
		}
	}
	if h.calls != 0 {
		t.Errorf("handler ran %d times for invalid keys", h.calls)
	}
}

func TestKeyWithoutSubjectIsUnauthenticated(t *testing.T) {
	h := newHarness()
	icpt := UnaryServerInterceptor(Options{Store: h.store, Subject: func(context.Context) string { return "" }})
	_, err := icpt(withKey("k"), cancelReq("op-a"), &grpc.UnaryServerInfo{FullMethod: cancelMethod}, h.handler)
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("err = %v, want UNAUTHENTICATED", err)
	}
}

// The loser of a race gets ErrDuplicateRequest from Create; the client still
// receives the winner's Operation.
func TestDuplicateRequestReturnsWinner(t *testing.T) {
	h := newHarness()
	hash, err := RequestHash(cancelMethod, cancelReq("op-a"))
	if err != nil {
		t.Fatal(err)
	}
	h.handler = func(context.Context, any) (any, error) {
		h.store.put("usr-1", "k", hash, &operationv1.Operation{Id: "op-winner"}) // committed meanwhile
		return nil, fmt.Errorf("create instance: %w", ErrDuplicateRequest)
	}
	op, err := h.call(withKey("k"), cancelMethod, cancelReq("op-a"))
	if err != nil || op.GetId() != "op-winner" {
		t.Errorf("got %v, %v; want op-winner", op, err)
	}
}

func TestRequestHashDependsOnMethodAndBody(t *testing.T) {
	a, _ := RequestHash(cancelMethod, cancelReq("op-a"))
	b, _ := RequestHash(cancelMethod, cancelReq("op-b"))
	c, _ := RequestHash(getMethod, cancelReq("op-a"))
	a2, _ := RequestHash(cancelMethod, cancelReq("op-a"))
	if string(a) == string(b) || string(a) == string(c) || string(a) != string(a2) {
		t.Error("hash must be stable and differ by method and body")
	}
}

func TestIncomingHeaderMatcher(t *testing.T) {
	if k, ok := IncomingHeaderMatcher("idempotency-key"); !ok || k != MetadataKey {
		t.Errorf("Idempotency-Key → %q, %v", k, ok)
	}
	if _, ok := IncomingHeaderMatcher("Authorization"); ok {
		t.Error("other headers must not match")
	}
}
