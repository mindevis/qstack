// Package testenv provides real infrastructure for integration tests
// (build tag `integration`, spec 14 §1).
//
// PostgreSQL comes from QS_TEST_PG_DSN when set (the DevSpace dev pod points
// it at a server in the dev namespace); otherwise a PostgreSQL 17 container is
// started once per test binary with testcontainers (CI, machines with Docker).
package testenv

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// PostgresDSNEnv names the variable with an admin DSN (URL form) of an
// existing server; the role must be allowed to create databases.
const PostgresDSNEnv = "QS_TEST_PG_DSN"

// postgresImage matches production: PostgreSQL 17 on Debian 13.
const postgresImage = "postgres:17-trixie"

var (
	containerOnce sync.Once
	containerDSN  string
	containerErr  error
)

// Postgres returns the DSN of a new, empty database that is dropped when the
// test ends. Each call gets its own database, so tests may run in parallel.
func Postgres(t testing.TB) string {
	t.Helper()
	admin, err := adminDSN()
	if err != nil {
		t.Fatalf("testenv: no PostgreSQL: %v (set %s or run Docker)", err, PostgresDSNEnv)
	}

	name := "test_" + randomSuffix()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := execAdmin(ctx, admin, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("testenv: create database: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// FORCE terminates connections a test may have leaked.
		if err := execAdmin(ctx, admin, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("testenv: drop database %s: %v", name, err)
		}
	})

	dsn, err := withDatabase(admin, name)
	if err != nil {
		t.Fatalf("testenv: %v", err)
	}
	return dsn
}

func adminDSN() (string, error) {
	if dsn := os.Getenv(PostgresDSNEnv); dsn != "" {
		return dsn, nil
	}
	containerOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		c, err := tcpostgres.Run(ctx, postgresImage,
			tcpostgres.WithDatabase("postgres"),
			tcpostgres.WithUsername("postgres"),
			tcpostgres.WithPassword("postgres"),
			tcpostgres.BasicWaitStrategies(),
		)
		if err != nil {
			containerErr = fmt.Errorf("start %s container: %w", postgresImage, err)
			return
		}
		// The container is removed by the testcontainers reaper when the
		// test binary exits.
		containerDSN, containerErr = c.ConnectionString(ctx, "sslmode=disable")
	})
	return containerDSN, containerErr
}

func execAdmin(ctx context.Context, dsn, sql string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	_, err = conn.Exec(ctx, sql)
	return err
}

// withDatabase returns dsn (URL form) pointing at database name.
func withDatabase(dsn, name string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "", fmt.Errorf("%s must be a postgres:// URL", PostgresDSNEnv)
	}
	u.Path = "/" + name
	return u.String(), nil
}

func randomSuffix() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
