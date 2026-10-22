package operation

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/db"
	"github.com/mindevis/qstack/pkg/ids"
)

func TestMigrationsAreValid(t *testing.T) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ValidateMigrations(sub); err != nil {
		t.Fatal(err)
	}
}

// REQ-PLT-041: Create refuses metadata without the resource ID. Validation
// happens before any database access, so no transaction is needed.
func TestCreateValidatesSpec(t *testing.T) {
	s := New(nil, clock.Real(), ids.NewGenerator(clock.Real(), rand.Reader))
	res := ids.New(ids.Instance)
	for name, sp := range map[string]Spec{
		"metadata without resource id": {ResourceID: res, Description: "Create instance", Metadata: wrapperspb.String("other")},
		"nil metadata":                 {ResourceID: res, Description: "Create instance"},
		"bad resource id":              {ResourceID: "nope", Description: "x", Metadata: wrapperspb.String("nope")},
		"no description":               {ResourceID: res, Metadata: wrapperspb.String(res)},
	} {
		if _, err := s.Create(context.Background(), nil, sp); err == nil {
			t.Errorf("%s: Create succeeded", name)
		}
	}
}

func TestErrorStatus(t *testing.T) {
	zone := apierr.ZoneCapacity("ru-central1-a")
	for name, tc := range map[string]struct {
		cause error
		code  codes.Code
	}{
		"apierr kept":        {zone, codes.ResourceExhausted},
		"wrapped apierr":     {fmt.Errorf("place: %w", zone), codes.ResourceExhausted},
		"cancelled kept":     {status.Error(codes.Canceled, "operation cancelled"), codes.Canceled},
		"plain error hidden": {errors.New("libvirt: connection refused on host-17"), codes.Internal},
		"internal hidden":    {status.Error(codes.Internal, "pq: deadlock"), codes.Internal},
		"nil":                {nil, codes.Internal},
	} {
		st := errorStatus(tc.cause)
		if codes.Code(st.GetCode()) != tc.code {
			t.Errorf("%s: code %v, want %v", name, codes.Code(st.GetCode()), tc.code)
		}
		if tc.code == codes.Internal && (st.GetMessage() != "internal error" || strings.Contains(st.GetMessage(), "host")) {
			t.Errorf("%s: message %q leaks details", name, st.GetMessage())
		}
	}
}

func TestNowIsMicrosecondUTC(t *testing.T) {
	loc := time.FixedZone("x", 3*3600)
	s := New(nil, clock.NewFake(time.Date(2026, 10, 23, 12, 0, 0, 123456789, loc)), nil)
	if got := s.now(); got.Nanosecond() != 123456000 || got.Location() != time.UTC {
		t.Errorf("now() = %v", got)
	}
}
