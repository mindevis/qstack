//go:build integration

package db

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/mindevis/qstack/pkg/testenv"
)

func testMigrations(t *testing.T) fs.FS {
	t.Helper()
	sub, err := fs.Sub(os.DirFS("testdata"), "migrations")
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

func openMigrated(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pool, err := Open(ctx, Config{DSN: testenv.Postgres(t), ApplicationName: "qs-test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := Migrate(ctx, pool, testMigrations(t)); err != nil {
		t.Fatal(err)
	}
	return pool
}

// Spec 02 §7: forward-only migrations applied once.
func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	pool, err := Open(ctx, Config{DSN: testenv.Postgres(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	applied, err := Migrate(ctx, pool, testMigrations(t))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"0001_create_widgets.sql", "0002_add_widgets_size.sql"}; !slices.Equal(applied, want) {
		t.Errorf("first run applied %v, want %v", applied, want)
	}
	again, err := Migrate(ctx, pool, testMigrations(t))
	if err != nil || len(again) != 0 {
		t.Errorf("second run applied %v (err %v), want nothing", again, err)
	}
}

func TestSessionSettings(t *testing.T) {
	pool := openMigrated(t)
	var tz, app string
	if err := pool.QueryRow(context.Background(), "SELECT current_setting('TimeZone'), current_setting('application_name')").Scan(&tz, &app); err != nil {
		t.Fatal(err)
	}
	if tz != "UTC" || app != "qs-test" {
		t.Errorf("TimeZone=%q application_name=%q, want UTC and qs-test", tz, app)
	}
}

// Spec 02 §7: optimistic locking with a version column.
func TestOptimisticLocking(t *testing.T) {
	ctx := context.Background()
	pool := openMigrated(t)
	if _, err := pool.Exec(ctx, `INSERT INTO widgets (id, project_id, name) VALUES ('w1', 'prj-1', 'one')`); err != nil {
		t.Fatal(err)
	}

	update := func(version int64, name string) error {
		return ExpectRow(pool.Exec(ctx,
			`UPDATE widgets SET name = $1, version = version + 1, updated_at = now() WHERE id = 'w1' AND version = $2`,
			name, version))
	}
	// Two writers read version 1; the first wins, the second gets ErrConflict.
	if err := update(1, "first"); err != nil {
		t.Fatalf("first update: %v", err)
	}
	if err := update(1, "second"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update: err = %v, want ErrConflict", err)
	}
	var name string
	var version int64
	if err := pool.QueryRow(ctx, `SELECT name, version FROM widgets WHERE id = 'w1'`).Scan(&name, &version); err != nil {
		t.Fatal(err)
	}
	if name != "first" || version != 2 {
		t.Errorf("row = (%q, %d), want (first, 2)", name, version)
	}
}

func TestUniqueLiveName(t *testing.T) {
	ctx := context.Background()
	pool := openMigrated(t)
	insert := `INSERT INTO widgets (id, project_id, name) VALUES ($1, 'prj-1', 'same')`
	if _, err := pool.Exec(ctx, insert, "w1"); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, insert, "w2")
	if !IsUniqueViolation(err, "widgets_project_name_live") {
		t.Fatalf("duplicate live name: err = %v, want unique violation", err)
	}
	// After soft delete the name is free again.
	if _, err := pool.Exec(ctx, `UPDATE widgets SET deleted_at = now() WHERE id = 'w1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, insert, "w2"); err != nil {
		t.Errorf("reuse after soft delete: %v", err)
	}
	if IsUniqueViolation(errors.New("other"), "") {
		t.Error("IsUniqueViolation(non-pg error) = true")
	}
}

func TestInTx(t *testing.T) {
	ctx := context.Background()
	pool := openMigrated(t)
	count := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM widgets`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	insert := func(tx pgx.Tx, id string) error {
		_, err := tx.Exec(ctx, `INSERT INTO widgets (id, project_id, name) VALUES ($1, 'prj-1', $1)`, id)
		return err
	}

	if err := InTx(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error { return insert(tx, "ok") }); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("boom")
	if err := InTx(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		if err := insert(tx, "rolled-back"); err != nil {
			return err
		}
		return boom
	}); !errors.Is(err, boom) {
		t.Errorf("InTx error = %v, want boom", err)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic was swallowed")
			}
		}()
		_ = InTx(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
			_ = insert(tx, "panicked")
			panic("handler bug")
		})
	}()

	if n := count(); n != 1 {
		t.Errorf("rows = %d, want 1 (only the committed one)", n)
	}
	if s := pool.Stat(); s.AcquiredConns() != 0 {
		t.Errorf("leaked connections: %d acquired", s.AcquiredConns())
	}
}

func TestCheckAndMetrics(t *testing.T) {
	ctx := context.Background()
	pool := openMigrated(t)
	if err := Check(pool)(ctx); err != nil {
		t.Errorf("Check() = %v", err)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(NewPoolCollector(pool))
	if n, err := testutil.GatherAndCount(reg); err != nil || n != 7 {
		t.Errorf("pool metrics = %d (err %v), want 7", n, err)
	}

	pool.Close()
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := Check(pool)(cctx); err == nil {
		t.Error("Check() on a closed pool succeeded")
	}
}

func TestOpenFailures(t *testing.T) {
	ctx := context.Background()
	if _, err := Open(ctx, Config{DSN: "postgres://%zz"}); err == nil || err.Error() != "db: invalid DSN" {
		t.Errorf("bad DSN: err = %v, want a message without the DSN", err)
	}
	if _, err := Open(ctx, Config{DSN: "postgres://u:secret@127.0.0.1:1/x?sslmode=disable", ConnectTimeout: time.Second}); err == nil {
		t.Error("unreachable server: want error")
	}
}
