//go:build integration

package pagination_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	operationv1 "github.com/mindevis/qstack/gen/go/qstack/operation/v1"
	"github.com/mindevis/qstack/pkg/clock"
	"github.com/mindevis/qstack/pkg/db"
	"github.com/mindevis/qstack/pkg/filter"
	"github.com/mindevis/qstack/pkg/pagination"
	"github.com/mindevis/qstack/pkg/testenv"
)

var (
	schema = filter.Schema{
		"name":   {Column: "name"},
		"status": {Column: "status", Values: []string{"RUNNING", "STOPPED"}},
		"labels": {Column: "labels", Labels: true},
	}
	sortable = map[string]pagination.SortField{
		"create_time": {Column: "created_at", Type: "timestamptz"},
		"name":        {Column: "name", Type: "text"},
		"id":          {Column: "id", Type: "text"},
	}
)

type widget struct {
	id, name  string
	createdAt time.Time
}

// seed inserts n widgets; groups of 7 share created_at to exercise the id
// tiebreaker across page boundaries.
func seed(t *testing.T, n int) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pool, err := db.Open(ctx, db.Config{DSN: testenv.Postgres(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `CREATE TABLE widgets (
		id text PRIMARY KEY, name text NOT NULL, status text NOT NULL,
		labels jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	rows := make([][]any, n)
	for i := range n {
		status, env := "RUNNING", "prod"
		if i%3 == 0 {
			status = "STOPPED"
		}
		if i%4 == 0 {
			env = "dev"
		}
		rows[i] = []any{fmt.Sprintf("wdg-%04d", (i*37)%n), fmt.Sprintf("w-%03d", i%50), status,
			map[string]string{"env": env}, base.Add(time.Duration(i/7) * time.Millisecond)}
	}
	if _, err := pool.CopyFrom(ctx, pgx.Identifier{"widgets"}, []string{"id", "name", "status", "labels", "created_at"},
		pgx.CopyFromRows(rows)); err != nil {
		t.Fatal(err)
	}
	return pool
}

// listPage is what a service's List handler does with the two packages.
func listPage(ctx context.Context, pool *pgxpool.Pool, s *pagination.Signer, req *operationv1.ListOperationsRequest, orderBy string) ([]widget, string, error) {
	size, err := pagination.PageSize(req.GetPageSize())
	if err != nil {
		return nil, "", err
	}
	f, err := filter.Parse(req.GetFilter(), schema)
	if err != nil {
		return nil, "", err
	}
	order, err := pagination.ParseOrder(orderBy, sortable, "")
	if err != nil {
		return nil, "", err
	}
	binding := pagination.Binding(req)
	var cursor []string
	if req.GetPageToken() != "" {
		if cursor, err = s.Decode(req.GetPageToken(), binding); err != nil {
			return nil, "", err
		}
	}
	where, args := f.SQL(1)
	after, afterArgs, err := order.After(cursor, len(args)+1)
	if err != nil {
		return nil, "", err
	}
	args = append(args, afterArgs...)
	q := fmt.Sprintf(`SELECT id, name, created_at FROM widgets WHERE %s AND %s ORDER BY %s LIMIT %d`,
		where, after, order.SQL(), size+1)
	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	ws, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (widget, error) {
		var w widget
		return w, r.Scan(&w.id, &w.name, &w.createdAt)
	})
	if err != nil {
		return nil, "", err
	}
	ws, more := pagination.Trim(ws, size)
	if !more {
		return ws, "", nil
	}
	last := ws[len(ws)-1]
	keys := make([]string, len(order))
	for i, k := range order {
		switch k.Field {
		case "create_time":
			keys[i] = pagination.TimeKey(last.createdAt)
		case "name":
			keys[i] = last.name
		case "id":
			keys[i] = last.id
		}
	}
	tok, err := s.Encode(binding, keys)
	return ws, tok, err
}

// REQ-PLT-031, REQ-PLT-032: walking all pages returns exactly the filtered
// rows in the requested order, without gaps or duplicates.
func TestWalkAllPages(t *testing.T) {
	ctx := context.Background()
	pool := seed(t, 500)
	s, err := pagination.NewSigner(clock.Real(), []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ filter, orderBy, wantSQL string }{
		{"", "", `TRUE ORDER BY created_at, id`},
		{`status = RUNNING AND labels.env != dev`, "create_time desc", `status = 'RUNNING' AND labels->>'env' IS DISTINCT FROM 'dev' ORDER BY created_at DESC, id DESC`},
		{`name IN ("w-001", "w-002", 'w-049')`, "name, id", `name IN ('w-001','w-002','w-049') ORDER BY name, id`},
		{`labels.missing = x`, "", `labels->>'missing' = 'x' ORDER BY created_at, id`},
	} {
		var want []string
		rows, err := pool.Query(ctx, `SELECT id FROM widgets WHERE `+tc.wantSQL)
		if err != nil {
			t.Fatal(err)
		}
		if want, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			t.Fatal(err)
		}

		var got []string
		token := ""
		for page := 0; ; page++ {
			if page > 100 {
				t.Fatal("too many pages")
			}
			req := &operationv1.ListOperationsRequest{
				Scope:  &operationv1.ListOperationsRequest_ProjectId{ProjectId: "prj-1"},
				Filter: tc.filter, PageSize: int32(13 + page%3), PageToken: token, // page size may vary
			}
			ws, next, err := listPage(ctx, pool, s, req, tc.orderBy)
			if err != nil {
				t.Fatalf("%q page %d: %v", tc.filter, page, err)
			}
			for _, w := range ws {
				got = append(got, w.id)
			}
			if next == "" {
				break
			}
			token = next
		}
		if !slices.Equal(got, want) {
			t.Errorf("filter %q order %q: got %d ids, want %d (first mismatch %v)", tc.filter, tc.orderBy, len(got), len(want), firstDiff(got, want))
		}
	}
}

func firstDiff(a, b []string) string {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return fmt.Sprintf("at %d: %s vs %s", i, a[i], b[i])
		}
	}
	return "length"
}
