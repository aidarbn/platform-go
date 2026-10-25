package postgres_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aidarbn/platform-go/kit/modules/postgres"
	"github.com/aidarbn/platform-go/kit/pgdb"
	"github.com/aidarbn/platform-go/kit/platform"
)

// ownDatabase creates a database for one test and drops it afterwards.
func ownDatabase(t *testing.T) string {
	t.Helper()
	base := os.Getenv("DATABASE_TEST_URL")
	if base == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgdb.Open(ctx, pgdb.Config{URL: base})
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("postgres_module_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		admin.Close()
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

var orderMigrations = fstest.MapFS{
	"20260101000001_orders.sql": {Data: []byte("-- +goose Up\nCREATE TABLE orders (id int PRIMARY KEY);\n")},
}

// Init applies the project's migrations and gives the code both pgx and database/sql
// over one pool; InTx runs on that pool.
func TestInitAppliesMigrationsAndProvidesSQL(t *testing.T) {
	dsn := ownDatabase(t)
	ctx := context.Background()
	app := platform.NewApp(nil)
	m := postgres.New(postgres.Config{URL: dsn, Migrate: true}, postgres.WithMigrations(orderMigrations))
	if err := m.Init(ctx, app); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(ctx)

	err := postgres.InTx(ctx, app, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO orders VALUES (1)")
		return err
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}
	var n int
	if err := postgres.SQL(app).QueryRowContext(ctx, "SELECT count(*) FROM orders").Scan(&n); err != nil || n != 1 {
		t.Fatalf("database/sql sees %d rows, %v", n, err)
	}
}

func TestInitSkipsMigrationsWhenTurnedOff(t *testing.T) {
	dsn := ownDatabase(t)
	ctx := context.Background()
	app := platform.NewApp(nil)
	m := postgres.New(postgres.Config{URL: dsn}, postgres.WithMigrations(orderMigrations))
	if err := m.Init(ctx, app); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(ctx)
	var exists bool
	if err := postgres.Pool(app).QueryRow(ctx, "SELECT to_regclass('orders') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatalf("orders exists = %v, %v; migrations must wait for the deploy step", exists, err)
	}
}

func TestInitStopsOnABrokenMigration(t *testing.T) {
	dsn := ownDatabase(t)
	ctx := context.Background()
	broken := fstest.MapFS{"20260101000001_broken.sql": {Data: []byte("-- +goose Up\nCREATE TABLE ;\n")}}
	m := postgres.New(postgres.Config{URL: dsn, Migrate: true}, postgres.WithMigrations(broken))
	defer m.Stop(ctx)
	if err := m.Init(ctx, platform.NewApp(nil)); err == nil || !strings.Contains(err.Error(), "migrations") {
		t.Fatalf("err = %v", err)
	}
}

// A second pool in one container would publish the same metric names.
func TestInitRefusesASecondPoolInOneApp(t *testing.T) {
	dsn := ownDatabase(t)
	ctx := context.Background()
	app := platform.NewApp(nil)
	first, second := postgres.New(postgres.Config{URL: dsn}), postgres.New(postgres.Config{URL: dsn})
	defer first.Stop(ctx)
	defer second.Stop(ctx)
	if err := first.Init(ctx, app); err != nil {
		t.Fatal(err)
	}
	if err := second.Init(ctx, app); err == nil || !strings.Contains(err.Error(), "pool metrics") {
		t.Fatalf("err = %v", err)
	}
}
