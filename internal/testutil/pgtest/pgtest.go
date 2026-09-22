// Package pgtest provides a migrated, isolated PostgreSQL database for tests.
package pgtest

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// NewPool migrates a fresh, uniquely named schema in TEST_DATABASE_URL and
// returns a pool whose connections use it. The schema is dropped when the test
// ends, so tests never touch existing tables and can share one database. The
// test is skipped when TEST_DATABASE_URL is unset.
func NewPool(t testing.TB) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping database test")
	}
	ctx := context.Background()

	schema := pgx.Identifier{"test_" + uuid.NewString()[:8]}
	exec(t, url, "CREATE SCHEMA "+schema.Sanitize())
	t.Cleanup(func() { exec(t, url, "DROP SCHEMA "+schema.Sanitize()+" CASCADE") })

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parsing TEST_DATABASE_URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema[0]

	sqlDB := stdlib.OpenDB(*cfg.ConnConfig)
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, os.DirFS(migrationsDir(t)))
	if err != nil {
		t.Fatalf("creating migration provider: %v", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("applying migrations: %v", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("creating pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func exec(t testing.TB, url, sql string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connecting to test database: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func migrationsDir(t testing.TB) string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate pgtest source file")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "db", "migrations")
}
