package gen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aidarbn/platform-go/internal/gen"
)

func TestNewMigration(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 9, 14, 12, 30, 5, 0, time.FixedZone("Almaty", 5*3600))

	path, err := gen.NewMigration(dir, "Create Orders!", at)
	if err != nil {
		t.Fatalf("NewMigration: %v", err)
	}
	// The name is in UTC, so developers in different zones order files the same way.
	if path != "db/migrations/20260914073005_create_orders.sql" {
		t.Errorf("path = %s", path)
	}
	raw, err := os.ReadFile(filepath.Join(dir, path))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "-- +goose Up") || !strings.Contains(string(raw), "-- +goose Down") {
		t.Errorf("content:\n%s", raw)
	}

	// The same name at the same second is refused rather than overwritten.
	if _, err := gen.NewMigration(dir, "create orders", at); err == nil {
		t.Error("an existing migration was overwritten")
	}
	for _, name := range []string{"", "  ", "!!!"} {
		if _, err := gen.NewMigration(dir, name, at); err == nil {
			t.Errorf("name %q was accepted", name)
		}
	}
}

func TestMigrationsPackageIsGeneratedWithPostgres(t *testing.T) {
	files, err := gen.Wiring(mustParse(t, withPostgres))
	if err != nil {
		t.Fatalf("Wiring: %v", err)
	}
	got := string(files[gen.MigrationsPath])
	if sqlc := string(files[gen.SqlcPath]); !strings.Contains(sqlc, "sql_package: pgx/v5") || !strings.Contains(sqlc, "out: ../internal/db/sqlcgen") {
		t.Errorf("sqlc.yaml:\n%s", sqlc)
	}
	for _, want := range []string{"package migrations", "//go:embed *", "var FS embed.FS"} {
		if !strings.Contains(got, want) {
			t.Errorf("migrations.gen.go lacks %q:\n%s", want, got)
		}
	}

	plain, err := gen.Wiring(mustParse(t, "schema: 1\nproject:\n  module: github.com/x/app\n"))
	if err != nil {
		t.Fatalf("Wiring: %v", err)
	}
	if _, ok := plain[gen.MigrationsPath]; ok {
		t.Error("migrations are generated without the postgres module")
	}
}
