package pgdb_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aidarbn/platform-go/kit/pgdb"
)

func TestMigrateSchemaChecksSteps(t *testing.T) {
	// Steps are checked before the database is touched, so a nil one is never used.
	for name, steps := range map[string][]pgdb.SchemaStep{
		"gap":        {{Version: 1}, {Version: 3}},
		"from zero":  {{Version: 0}},
		"duplicated": {{Version: 1}, {Version: 1}},
	} {
		if err := pgdb.MigrateSchema(context.Background(), nil, "m", steps); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if err := pgdb.MigrateSchema(context.Background(), nil, "", nil); err == nil {
		t.Error("empty owner: no error")
	}
}

// A module's tables against a real database: each step applied once, a new step applied
// on the next start, a newer database left alone, a failed step rolled back whole, and
// instances starting together applying a step exactly once.
func TestMigrateSchemaWithRealDatabase(t *testing.T) {
	pool := schemaTestDatabase(t)
	ctx := context.Background()

	v1 := pgdb.SchemaStep{Version: 1, Statements: []string{
		"CREATE TABLE notes (id bigserial PRIMARY KEY)",
	}}
	v2 := pgdb.SchemaStep{Version: 2, Statements: []string{
		"ALTER TABLE notes ADD COLUMN body text NOT NULL DEFAULT ''",
		"CREATE INDEX notes_body_idx ON notes (body)",
	}}

	// Without IF NOT EXISTS a second run would fail, so passing twice proves the skip.
	for range 2 {
		if err := pgdb.MigrateSchema(ctx, pool, "notes", []pgdb.SchemaStep{v1}); err != nil {
			t.Fatalf("MigrateSchema v1: %v", err)
		}
	}
	assertVersion(t, pool, "notes", 1)

	if err := pgdb.MigrateSchema(ctx, pool, "notes", []pgdb.SchemaStep{v1, v2}); err != nil {
		t.Fatalf("MigrateSchema v2: %v", err)
	}
	assertVersion(t, pool, "notes", 2)
	if _, err := pool.Exec(ctx, "INSERT INTO notes (body) VALUES ('x')"); err != nil {
		t.Fatalf("column of v2 missing: %v", err)
	}

	// A rollback to the release before: its steps stop at v1, the database stays at v2.
	if err := pgdb.MigrateSchema(ctx, pool, "notes", []pgdb.SchemaStep{v1}); err != nil {
		t.Fatalf("older release: %v", err)
	}
	assertVersion(t, pool, "notes", 2)

	// A failing step leaves neither its tables nor a version behind.
	broken := []pgdb.SchemaStep{{Version: 1, Statements: []string{
		"CREATE TABLE half (id int)",
		"SELECT * FROM no_such_table",
	}}}
	if err := pgdb.MigrateSchema(ctx, pool, "broken", broken); err == nil {
		t.Fatal("broken step: no error")
	}
	assertVersion(t, pool, "broken", 0)
	var half bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('half') IS NOT NULL").Scan(&half); err != nil || half {
		t.Fatalf("table of a failed step left behind: %v, %v", half, err)
	}

	// Several instances at once, on an empty versions row of their own.
	steps := []pgdb.SchemaStep{{Version: 1, Statements: []string{"CREATE TABLE racers (id int)"}}}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { errs <- pgdb.MigrateSchema(ctx, pool, "racers", steps) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent MigrateSchema: %v", err)
		}
	}
	assertVersion(t, pool, "racers", 1)
}

func assertVersion(t *testing.T, pool *pgxpool.Pool, owner string, want int) {
	t.Helper()
	var got int
	err := pool.QueryRow(context.Background(),
		"SELECT coalesce((SELECT version FROM "+pgdb.SchemaVersionsTable+" WHERE owner = $1), 0)", owner).Scan(&got)
	if err != nil {
		t.Fatalf("read version of %s: %v", owner, err)
	}
	if got != want {
		t.Fatalf("version of %s = %d, want %d", owner, got, want)
	}
}

// schemaTestDatabase returns a pool on a database of its own, so the versions table
// starts empty on every run.
func schemaTestDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_TEST_URL")
	if url == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}
	ctx := context.Background()

	admin, err := pgdb.Open(ctx, pgdb.Config{URL: url})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(admin.Close)
	name := fmt.Sprintf("schema_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})

	cfg, err := pgxConfigFor(url, name)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgdb.Open(ctx, pgdb.Config{URL: cfg})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
