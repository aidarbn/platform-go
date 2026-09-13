package pgdb_test

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/aidarbn/platform-go/kit/pgdb"
)

func TestMigrateWithoutFilesDoesNothing(t *testing.T) {
	// No SQL files means no database work at all, so a nil pool is never touched.
	fsys := fstest.MapFS{"migrations.gen.go": {Data: []byte("package migrations")}}
	applied, err := pgdb.Migrate(context.Background(), nil, fsys, nil)
	if err != nil || len(applied) != 0 {
		t.Fatalf("Migrate = %v, %v", applied, err)
	}
}

// Migrations against a real database: applied once, skipped on the next start, the Go
// file of the embedded directory ignored, and two instances starting together do not
// apply anything twice.
func TestMigrateWithRealDatabase(t *testing.T) {
	url := os.Getenv("DATABASE_TEST_URL")
	if url == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}
	ctx := context.Background()

	// A database of its own, so the goose version table starts empty on every run.
	admin, err := pgdb.Open(ctx, pgdb.Config{URL: url})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer admin.Close()
	name := fmt.Sprintf("migrate_test_%d", time.Now().UnixNano())
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
	defer pool.Close()

	fsys := fstest.MapFS{
		"migrations.gen.go": {Data: []byte("package migrations")},
		"20260914000001_create_orders.sql": {Data: []byte(`-- +goose Up
CREATE TABLE orders (id bigserial PRIMARY KEY, total bigint NOT NULL);
-- +goose Down
DROP TABLE orders;
`)},
		"20260914000002_add_status.sql": {Data: []byte(`-- +goose Up
ALTER TABLE orders ADD COLUMN status text NOT NULL DEFAULT 'new';
-- +goose Down
ALTER TABLE orders DROP COLUMN status;
`)},
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results [][]string
		errs    []error
	)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			applied, err := pgdb.Migrate(ctx, pool, fsys, nil)
			mu.Lock()
			defer mu.Unlock()
			results = append(results, applied)
			errs = append(errs, err)
		}()
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			t.Fatalf("Migrate: %v", err)
		}
	}
	all := slices.Concat(results...)
	slices.Sort(all)
	want := []string{"20260914000001_create_orders.sql", "20260914000002_add_status.sql"}
	if !slices.Equal(all, want) {
		t.Errorf("applied across both instances = %v, want each file exactly once", all)
	}

	if _, err := pool.Exec(ctx, "INSERT INTO orders (total) VALUES (100)"); err != nil {
		t.Fatalf("the schema is not there: %v", err)
	}
	var status string
	if err := pool.QueryRow(ctx, "SELECT status FROM orders").Scan(&status); err != nil || status != "new" {
		t.Errorf("status = %q, %v", status, err)
	}

	again, err := pgdb.Migrate(ctx, pool, fsys, nil)
	if err != nil || len(again) != 0 {
		t.Errorf("second start applied %v, %v", again, err)
	}

	// A broken migration stops the start and names the file.
	fsys["20260914000003_broken.sql"] = &fstest.MapFile{Data: []byte("-- +goose Up\nCREATE TABLE ;\n")}
	if _, err := pgdb.Migrate(ctx, pool, fsys, nil); err == nil {
		t.Error("a broken migration was accepted")
	}
}
