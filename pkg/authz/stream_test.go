package authz

import (
	"context"
	"log/slog"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type sendRecorder struct {
	grpc.ServerStream
	sent int
}

func (s *sendRecorder) SendMsg(any) error { s.sent++; return nil }

// A stream sends nothing before an allowing check.
func TestStreamBlocksSendBeforeCheck(t *testing.T) {
	c := &call{}
	inner := &sendRecorder{}
	s := &checkedStream{ServerStream: inner, ctx: context.Background(), call: c,
		opts: &Options{Logger: slog.New(slog.DiscardHandler)}, method: "/x.Y/Z"}
	if err := s.SendMsg("data"); status.Code(err) != codes.Internal || inner.sent != 0 {
		t.Fatalf("send before check: %v, sent %d", err, inner.sent)
	}
	c.record(true)
	if err := s.SendMsg("data"); err != nil || inner.sent != 1 {
		t.Errorf("send after allow: %v, sent %d", err, inner.sent)
	}
	c.record(false)
	if err := s.SendMsg("data"); status.Code(err) != codes.Internal || inner.sent != 1 {
		t.Errorf("send after a denial: %v, sent %d", err, inner.sent)
	}
}
