package outbox

import (
	"context"
	"crypto/rand"
	"io/fs"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/db"
	"github.com/mindevis/qstack/pkg/events"
	"github.com/mindevis/qstack/pkg/ids"
)

type fakeTx struct{ args [][]any }

func (f *fakeTx) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	f.args = append(f.args, args)
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func newWriter(t *testing.T) *Writer {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC))
	w, err := NewWriter(events.Source{Region: "ru-central1", Service: "compute"}, clk, ids.NewGenerator(clk, rand.Reader))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestMigrationsAreValid(t *testing.T) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ValidateMigrations(sub); err != nil {
		t.Fatal(err)
	}
}

// REQ-PLT-090: a resource change becomes a complete CloudEvent on the
// resource subject.
func TestResourceChanged(t *testing.T) {
	w := newWriter(t)
	tx := &fakeTx{}
	ev, err := w.ResourceChanged(context.Background(), tx, ResourceChange{
		Resource: "instance", Change: events.Updated, ResourceID: "ins-01m5hqpzg00000000000000000",
		ProjectID: "prj-01m5hqpzg00000000000000000", Version: 7, Data: wrapperspb.String("vm"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if ids.ParseKind(ev.ID, ids.Event) != nil || ev.Type != "qstack.compute.instance.updated" ||
		ev.Source != "qstack://compute/ru-central1" || ev.ResourceVersion != 7 || ev.Time.IsZero() {
		t.Errorf("event %+v", ev)
	}
	if len(tx.args) != 1 || tx.args[0][0] != ev.ID || tx.args[0][1] != "qs.ru-central1.compute.instance.updated" {
		t.Fatalf("insert args %v", tx.args)
	}
	stored, err := events.Unmarshal(tx.args[0][2].([]byte))
	if err != nil || stored.ID != ev.ID {
		t.Errorf("stored payload: %+v, %v", stored, err)
	}
}

func TestWriterRejectsInvalidInput(t *testing.T) {
	w := newWriter(t)
	ok := ResourceChange{Resource: "instance", Change: events.Created, ResourceID: "ins-1", Data: wrapperspb.String("x")}
	for name, mutate := range map[string]func(*ResourceChange){
		"resource with dot": func(rc *ResourceChange) { rc.Resource = "a.b" },
		"unknown change":    func(rc *ResourceChange) { rc.Change = "moved" },
		"no id":             func(rc *ResourceChange) { rc.ResourceID = "" },
		"no data":           func(rc *ResourceChange) { rc.Data = nil },
	} {
		rc := ok
		mutate(&rc)
		if _, err := w.ResourceChanged(context.Background(), &fakeTx{}, rc); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, subj := range []string{"usage.x", "qs.ru-central1.usage.*", "qs.a b"} {
		if _, err := w.Add(context.Background(), &fakeTx{}, subj, events.Event{Type: "t"}); err == nil {
			t.Errorf("subject %q accepted", subj)
		}
	}
	if _, err := NewWriter(events.Source{Region: "x.y", Service: "compute"}, clock.Real(), nil); err == nil {
		t.Error("invalid source accepted")
	}
}
