package pagination

import (
	"strings"
	"testing"
	"time"

	apipb "github.com/mindevis/qstack/gen/go/qstack/api"
	operationv1 "github.com/mindevis/qstack/gen/go/qstack/operation/v1"
	"github.com/mindevis/qstack/pkg/apierr"
	"github.com/mindevis/qstack/pkg/clock"
)

var key = []byte(strings.Repeat("k", 32))

func isInvalid(err error) bool { return apierr.IsReason(err, apipb.ErrorReason_QS_INVALID_FIELD) }

// REQ-PLT-031: default 100, max 1000.
func TestPageSize(t *testing.T) {
	for in, want := range map[int32]int{0: 100, 1: 1, 1000: 1000, 5000: 1000} {
		if got, err := PageSize(in); got != want || err != nil {
			t.Errorf("PageSize(%d) = %d, %v", in, got, err)
		}
	}
	if _, err := PageSize(-1); !isInvalid(err) {
		t.Error("negative page size accepted")
	}
	rows, more := Trim([]int{1, 2, 3}, 2)
	if len(rows) != 2 || !more {
		t.Error("Trim")
	}
}

func list(project, filter string, size int32, token string) *operationv1.ListOperationsRequest {
	return &operationv1.ListOperationsRequest{
		Scope:  &operationv1.ListOperationsRequest_ProjectId{ProjectId: project},
		Filter: filter, PageSize: size, PageToken: token,
	}
}

// REQ-PLT-031: the token is signed, bound to the request parameters (not to
// page_size) and expires after an hour.
func TestTokens(t *testing.T) {
	clk := clock.NewFake(time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC))
	s, err := NewSigner(clk, key)
	if err != nil {
		t.Fatal(err)
	}
	b := Binding(list("prj-a", "done = true", 10, ""))
	tok, err := s.Encode(b, []string{"2026-10-23T12:00:00Z", "op-1"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Decode(tok, Binding(list("prj-a", "done = true", 50, tok)))
	if err != nil || len(got) != 2 || got[1] != "op-1" {
		t.Fatalf("Decode = %v, %v (page_size and page_token must not matter)", got, err)
	}

	if _, err := s.Decode(tok, Binding(list("prj-a", "done = false", 10, ""))); !isInvalid(err) {
		t.Error("token accepted with another filter")
	}
	if _, err := s.Decode(tok, Binding(list("prj-b", "done = true", 10, ""))); !isInvalid(err) {
		t.Error("token accepted for another project")
	}
	tampered := "AAAA" + tok[4:]
	if _, err := s.Decode(tampered, b); !isInvalid(err) {
		t.Error("tampered token accepted")
	}
	for _, junk := range []string{"", ".", "abc", "a.b.c", tok + "x"} {
		if _, err := s.Decode(junk, b); !isInvalid(err) {
			t.Errorf("Decode(%q) accepted", junk)
		}
	}
	other, _ := NewSigner(clk, []byte(strings.Repeat("o", 32)))
	if _, err := other.Decode(tok, b); !isInvalid(err) {
		t.Error("token accepted with another key")
	}
	rotated, _ := NewSigner(clk, []byte(strings.Repeat("n", 32)), key)
	if _, err := rotated.Decode(tok, b); err != nil {
		t.Errorf("token signed with the previous key rejected after rotation: %v", err)
	}

	clk.Advance(TokenTTL - time.Second)
	if _, err := s.Decode(tok, b); err != nil {
		t.Errorf("token rejected before expiry: %v", err)
	}
	clk.Advance(time.Second)
	if _, err := s.Decode(tok, b); !isInvalid(err) {
		t.Error("expired token accepted")
	}
	if _, err := NewSigner(clk, []byte("short")); err == nil {
		t.Error("short key accepted")
	}
}

var sortable = map[string]SortField{
	"create_time": {Column: "created_at", Type: "timestamptz"},
	"name":        {Column: "name", Type: "text"},
	"id":          {Column: "id", Type: "text"},
}

func TestParseOrder(t *testing.T) {
	for in, want := range map[string]string{
		"":                  `"created_at", "id"`,
		"name":              `"name", "id"`,
		"create_time desc":  `"created_at" DESC, "id" DESC`,
		"name DESC, id asc": `"name" DESC, "id"`,
	} {
		o, err := ParseOrder(in, sortable, "")
		if err != nil || o.SQL() != want {
			t.Errorf("ParseOrder(%q).SQL = %q, %v; want %q", in, o.SQL(), err, want)
		}
	}
	for _, in := range []string{"size", "name sideways", "name, name", "id, name", "name desc asc", ","} {
		if _, err := ParseOrder(in, sortable, ""); !isInvalid(err) {
			t.Errorf("ParseOrder(%q) accepted", in)
		}
	}
}

func TestAfter(t *testing.T) {
	o, _ := ParseOrder("create_time desc", sortable, "")
	sql, args, err := o.After([]string{"2026-10-23T12:00:00Z", "ins-1"}, 4)
	want := `(("created_at" < $4::timestamptz) OR ("created_at" = $4::timestamptz AND "id" < $5::text))`
	if err != nil || sql != want || len(args) != 2 {
		t.Errorf("After = %s %v %v\nwant %s", sql, args, err, want)
	}
	if sql, _, _ := o.After(nil, 1); sql != "TRUE" {
		t.Error("empty cursor")
	}
	if _, _, err := o.After([]string{"x"}, 1); !isInvalid(err) {
		t.Error("cursor of another order accepted")
	}
}
