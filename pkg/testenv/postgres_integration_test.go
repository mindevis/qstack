//go:build integration

package testenv

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestPostgresGivesIsolatedDatabases(t *testing.T) {
	ctx := context.Background()
	a, b := Postgres(t), Postgres(t)
	if a == b {
		t.Fatal("two calls returned the same DSN")
	}

	connA, err := pgx.Connect(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connA.Close(ctx) }()
	if _, err := connA.Exec(ctx, "CREATE TABLE only_in_a (id int)"); err != nil {
		t.Fatal(err)
	}

	connB, err := pgx.Connect(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connB.Close(ctx) }()
	var exists bool
	if err := connB.QueryRow(ctx, "SELECT to_regclass('only_in_a') IS NOT NULL").Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("table from database A is visible in database B")
	}

	var version int
	if err := connA.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < 170000 {
		t.Errorf("server_version_num = %d, want PostgreSQL 17+", version)
	}
}
