// Package db is the PostgreSQL access layer shared by all services (spec 02 §7,
// ADR-0004): a pgx connection pool, transactions, optimistic locking,
// forward-only migrations and pool metrics.
//
// Every service owns its own database and role (REQ-PLT-080); nothing in this
// package crosses that boundary.
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Config configures the connection pool.
type Config struct {
	// DSN is a PostgreSQL URL or key/value string. It contains a password:
	// never log it.
	DSN string
	// ApplicationName is reported to PostgreSQL (pg_stat_activity).
	ApplicationName string
	// MaxConns and MinConns bound the pool size; 0 keeps the pgx default.
	MaxConns int32
	MinConns int32
	// MaxConnLifetime recycles connections, e.g. after a primary switchover.
	MaxConnLifetime time.Duration
	// ConnectTimeout bounds Open's initial ping; default 10s.
	ConnectTimeout time.Duration
}

// Open creates a pool and verifies connectivity.
func Open(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		// pgx errors may echo the DSN; do not wrap them.
		return nil, errors.New("db: invalid DSN")
	}
	if cfg.ApplicationName != "" {
		pc.ConnConfig.RuntimeParams["application_name"] = cfg.ApplicationName
	}
	// Timestamps are always UTC (spec 02 §7).
	pc.ConnConfig.RuntimeParams["timezone"] = "UTC"
	if cfg.MaxConns > 0 {
		pc.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		pc.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime > 0 {
		pc.MaxConnLifetime = cfg.MaxConnLifetime
	}

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("db: create pool: %w", err)
	}
	timeout := cfg.ConnectTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := pool.Ping(pctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

// Beginner is satisfied by *pgxpool.Pool and pgx.Tx (nested transactions
// become savepoints).
type Beginner interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// InTx runs fn in a transaction: committed if fn returns nil, rolled back if
// it returns an error or panics (the panic is re-raised).
func InTx(ctx context.Context, b Beginner, opts pgx.TxOptions, fn func(tx pgx.Tx) error) (err error) {
	tx, err := b.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("db: begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: commit: %w", err)
	}
	return nil
}

// ErrConflict means an optimistic-locking update matched no row: the row was
// changed (version bumped) or deleted concurrently. Services map it to
// apierr.EtagMismatch.
var ErrConflict = errors.New("db: version conflict")

// ExpectRow checks the result of a versioned write such as
//
//	UPDATE instances SET ..., version = version + 1 WHERE id = $1 AND version = $2
//
// and returns ErrConflict if no row was affected.
func ExpectRow(tag pgconn.CommandTag, err error) error {
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// IsUniqueViolation reports whether err is a unique-constraint violation,
// optionally on the named constraint (e.g. a resource name index).
func IsUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return constraint == "" || pgErr.ConstraintName == constraint
}

// Check returns a readiness check (telemetry.Health.AddCheck) that pings the pool.
func Check(pool *pgxpool.Pool) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		if err := pool.Ping(ctx); err != nil {
			return errors.New("database unreachable")
		}
		return nil
	}
}
