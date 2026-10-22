package ids

import (
	"bytes"
	"errors"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mindevis/qstack/pkg/clock"
)

// The same pattern as the proto validation rules (api/proto/qstack/...).
var protoIDPattern = regexp.MustCompile(`^[a-z]{2,4}-[0-9a-hjkmnp-tv-z]{26}$`)

// REQ-PLT-020: every kind generates IDs that parse back to the same kind.
func TestNewParseAllKinds(t *testing.T) {
	for _, k := range Kinds() {
		t.Run(string(k), func(t *testing.T) {
			id := New(k)
			if !strings.HasPrefix(id, k.Prefix()+"-") {
				t.Fatalf("New(%s) = %q, want prefix %q", k, id, k.Prefix()+"-")
			}
			if !protoIDPattern.MatchString(id) {
				t.Errorf("New(%s) = %q does not match the proto ID pattern", k, id)
			}
			got, err := Parse(id)
			if err != nil || got != k {
				t.Errorf("Parse(%q) = %q, %v; want %q, nil", id, got, err, k)
			}
			if k.Name() == "" {
				t.Errorf("kind %q has no name", k)
			}
		})
	}
}

func TestKindPrefixesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range Kinds() {
		if seen[k.Prefix()] {
			t.Errorf("duplicate prefix %q", k.Prefix())
		}
		seen[k.Prefix()] = true
	}
}

func TestDeterministicWithFakeClockAndEntropy(t *testing.T) {
	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	g := NewGenerator(clock.NewFake(at), bytes.NewReader(make([]byte, 10)))

	id := g.New(Instance)
	if id != "ins-01m5hqpzg00000000000000000" {
		t.Errorf("New() = %q", id)
	}
	ts, err := Time(id)
	if err != nil || !ts.Equal(at) {
		t.Errorf("Time(%q) = %v, %v; want %v", id, ts, err, at)
	}
}

func TestIDsSortByCreationTime(t *testing.T) {
	c := clock.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	g := NewGenerator(c, constReader(0xff)) // max entropy must not break ordering
	var list []string
	for range 5 {
		list = append(list, g.New(Disk))
		c.Advance(time.Millisecond)
	}
	if !slices.IsSorted(list) {
		t.Errorf("IDs are not sorted by time: %v", list)
	}
}

func TestNewIsUniqueAndConcurrencySafe(t *testing.T) {
	const n = 2000
	var (
		mu   sync.Mutex
		seen = make(map[string]struct{}, n)
		wg   sync.WaitGroup
	)
	for range n {
		wg.Go(func() {
			id := New(Operation)
			mu.Lock()
			seen[id] = struct{}{}
			mu.Unlock()
		})
	}
	wg.Wait()
	if len(seen) != n {
		t.Errorf("got %d unique IDs, want %d", len(seen), n)
	}
}

func TestParseRejects(t *testing.T) {
	valid := "ins-01m5hqpzg00000000000000000"
	tests := []struct {
		name string
		id   string
	}{
		{"empty", ""},
		{"no prefix", "01m5hqpzg00000000000000000"},
		{"unknown prefix", "xyz-01m5hqpzg00000000000000000"},
		{"too short", "ins-01m5hqpzg"},
		{"too long", valid + "0"},
		{"uppercase", "ins-01M5HQPZG00000000000000000"},
		{"excluded letter i", "ins-01m5hqpzg0000000000000000i"},
		{"excluded letter u", "ins-01m5hqpzg0000000000000000u"},
		{"overflow", "ins-81m5hqpzg00000000000000000"},
		{"double dash", "ins--1m5hqpzg00000000000000000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse(tt.id); !errors.Is(err, ErrInvalidID) {
				t.Errorf("Parse(%q) error = %v, want ErrInvalidID", tt.id, err)
			}
		})
	}
	if _, err := Parse(valid); err != nil {
		t.Fatalf("Parse(valid) = %v", err)
	}
}

func TestParseKind(t *testing.T) {
	id := New(Disk)
	if err := ParseKind(id, Disk); err != nil {
		t.Errorf("ParseKind(disk, Disk) = %v", err)
	}
	if err := ParseKind(id, Instance); !errors.Is(err, ErrInvalidID) {
		t.Errorf("ParseKind(disk, Instance) = %v, want ErrInvalidID", err)
	}
}

func TestNewPanicsOnUnknownKind(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("New(unknown) did not panic")
		}
	}()
	New(Kind("zzz"))
}

// FuzzParse checks that Parse never panics and that every accepted ID is in
// canonical form (decode→encode reproduces the input exactly).
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"ins-01m5hqpzg00000000000000000", "op-7zzzzzzzzzzzzzzzzzzzzzzzzz", "", "-", "ins-", "sa-0",
		"prj-81m5hqpzg00000000000000000", "ins-01M5HQPZG00000000000000000",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		k, err := Parse(s)
		if err != nil {
			return
		}
		_, ulid, _ := strings.Cut(s, "-")
		b, derr := decode(ulid)
		if derr != nil {
			t.Fatalf("Parse accepted %q but decode failed: %v", s, derr)
		}
		if again := k.Prefix() + "-" + encode(b); again != s {
			t.Fatalf("non-canonical ID accepted: %q re-encodes to %q", s, again)
		}
	})
}

type constReader byte

func (c constReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(c)
	}
	return len(p), nil
}
