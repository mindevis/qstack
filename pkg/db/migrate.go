package db

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// migrationName is NNNN_snake_case.sql, e.g. 0001_create_instances.sql.
var migrationName = regexp.MustCompile(`^[0-9]{4}_[a-z0-9_]+\.sql$`)

// ValidateMigrations checks the migration files in fsys (embed.FS of a
// service's store/migrations): names are NNNN_name.sql with consecutive
// numbers from 0001, every file has an Up section and none has a non-empty
// Down section — migrations only go forward (spec 02 §7, expand/contract).
func ValidateMigrations(fsys fs.FS) error {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return fmt.Errorf("db: read migrations: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !migrationName.MatchString(e.Name()) {
			return fmt.Errorf("db: migration %q: name must match NNNN_name.sql", e.Name())
		}
		names = append(names, e.Name())
	}
	if len(names) == 0 {
		return fmt.Errorf("db: no migrations found")
	}
	slices.Sort(names)
	for i, n := range names {
		if want := fmt.Sprintf("%04d_", i+1); !strings.HasPrefix(n, want) {
			return fmt.Errorf("db: migration %q: expected prefix %s (numbers must be consecutive)", n, want)
		}
		if err := checkForwardOnly(fsys, n); err != nil {
			return fmt.Errorf("db: migration %q: %w", n, err)
		}
	}
	return nil
}

// checkForwardOnly requires an Up section and rejects statements under Down.
func checkForwardOnly(fsys fs.FS, name string) error {
	f, err := fsys.Open(path.Clean(name))
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	var hasUp, inDown bool
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "-- +goose Up"):
			hasUp, inDown = true, false
		case strings.HasPrefix(line, "-- +goose Down"):
			inDown = true
		case inDown && line != "" && !strings.HasPrefix(line, "--"):
			return fmt.Errorf("has a Down section; migrations are forward-only")
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if !hasUp {
		return fmt.Errorf("missing '-- +goose Up' annotation")
	}
	return nil
}

// Migrate validates and applies all pending migrations from fsys and returns
// the names applied in this run.
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) ([]string, error) {
	if err := ValidateMigrations(fsys); err != nil {
		return nil, err
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()

	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fsys)
	if err != nil {
		return nil, fmt.Errorf("db: migrations: %w", err)
	}
	results, err := p.Up(ctx)
	if err != nil {
		return nil, fmt.Errorf("db: migrate: %w", err)
	}
	applied := make([]string, 0, len(results))
	for _, r := range results {
		applied = append(applied, path.Base(r.Source.Path))
	}
	return applied, nil
}
