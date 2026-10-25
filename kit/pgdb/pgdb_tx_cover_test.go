package pgdb_test

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aidarbn/platform-go/kit/pgdb"
)

func countRows(t *testing.T, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) int {
	t.Helper()
	var n int
	if err := q.QueryRow(context.Background(), "SELECT count(*) FROM tx_items").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// InTx against a real database: a clean return commits, an error or a panic rolls back,
// a failing commit is reported and a closed pool cannot begin.
func TestInTxWithRealDatabase(t *testing.T) {
	pool := schemaTestDatabase(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE tx_items (id int PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	insert := func(id int) func(pgx.Tx) error {
		return func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "INSERT INTO tx_items VALUES ($1)", id)
			return err
		}
	}

	if err := pgdb.InTx(ctx, pool, insert(1)); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if n := countRows(t, pool); n != 1 {
		t.Fatalf("rows after commit = %d", n)
	}

	boom := errors.New("boom")
	err := pgdb.InTx(ctx, pool, func(tx pgx.Tx) error {
		if err := insert(2)(tx); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the error of fn as it is", err)
	}
	if n := countRows(t, pool); n != 1 {
		t.Fatalf("rows after an error = %d, want the insert rolled back", n)
	}

	func() {
		defer func() {
			if r := recover(); r != "panic in tx" {
				t.Fatalf("recovered %v, want the panic passed through", r)
			}
		}()
		_ = pgdb.InTx(ctx, pool, func(tx pgx.Tx) error {
			_ = insert(3)(tx)
			panic("panic in tx")
		})
	}()
	if n := countRows(t, pool); n != 1 {
		t.Fatalf("rows after a panic = %d, want the insert rolled back", n)
	}
	// The connection came back to the pool usable.
	if err := pgdb.InTx(ctx, pool, insert(4)); err != nil {
		t.Fatalf("after a panic: %v", err)
	}

	// A deferred constraint is checked at commit, so Commit itself fails.
	if _, err := pool.Exec(ctx, `CREATE TABLE tx_parents (id int PRIMARY KEY);
CREATE TABLE tx_children (parent int REFERENCES tx_parents DEFERRABLE INITIALLY DEFERRED)`); err != nil {
		t.Fatal(err)
	}
	err = pgdb.InTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO tx_children VALUES (99)")
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "pgdb: commit transaction") {
		t.Fatalf("err = %v, want a commit error", err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := pgdb.InTx(cancelled, pool, insert(5)); err == nil || !strings.Contains(err.Error(), "pgdb: begin transaction") {
		t.Fatalf("err = %v, want a begin error", err)
	}
}

// failingTx fails the n-th statement of MigrateSchema: Exec and QueryRow calls are
// counted together, Commit is the last one.
type failingTx struct {
	pgx.Tx
	failAt, calls int
}

var errInjected = errors.New("injected")

func (f *failingTx) step() error {
	f.calls++
	if f.calls == f.failAt {
		return errInjected
	}
	return nil
}

func (f *failingTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, f.step()
}

func (f *failingTx) QueryRow(context.Context, string, ...any) pgx.Row {
	return fakeRow{err: f.step()}
}

func (f *failingTx) Commit(context.Context) error   { return f.step() }
func (f *failingTx) Rollback(context.Context) error { return nil }

// fakeRow reads no version: the owner is new.
type fakeRow struct{ err error }

func (r fakeRow) Scan(...any) error {
	if r.err != nil {
		return r.err
	}
	return pgx.ErrNoRows
}

type fakeBeginner struct {
	tx  *failingTx
	err error
}

func (b fakeBeginner) Begin(context.Context) (pgx.Tx, error) {
	if b.err != nil {
		return nil, b.err
	}
	return b.tx, nil
}

func TestMigrateSchemaReportsEveryFailedStep(t *testing.T) {
	steps := []pgdb.SchemaStep{{Version: 1, Statements: []string{"CREATE TABLE x ()"}}}
	ctx := context.Background()

	if err := pgdb.MigrateSchema(ctx, fakeBeginner{err: errInjected}, "m", steps); !errors.Is(err, errInjected) {
		t.Fatalf("begin: err = %v", err)
	}
	// Calls in order: lock, versions table, read version, the step, record version, commit.
	for at, want := range map[int]string{1: "lock", 2: "versions table", 3: "read version", 4: "version 1", 5: "record version", 6: "pgdb: schema m: injected"} {
		err := pgdb.MigrateSchema(ctx, fakeBeginner{tx: &failingTx{failAt: at}}, "m", steps)
		if !errors.Is(err, errInjected) || !strings.Contains(err.Error(), want) {
			t.Errorf("failing call %d: err = %v, want %q", at, err, want)
		}
	}
}

type unreadableFS struct{}

func (unreadableFS) Open(string) (fs.File, error) { return nil, fs.ErrPermission }

func TestMigrateEdgeCases(t *testing.T) {
	// A directory that cannot be read has no migrations to run.
	if applied, err := pgdb.Migrate(context.Background(), nil, unreadableFS{}, nil); err != nil || applied != nil {
		t.Fatalf("unreadable: %v, %v", applied, err)
	}

	pool := schemaTestDatabase(t)
	// Two files with one version: goose refuses before running anything.
	dup := fstest.MapFS{
		"1_a.sql": {Data: []byte("-- +goose Up\nSELECT 1;\n")},
		"1_b.sql": {Data: []byte("-- +goose Up\nSELECT 2;\n")},
	}
	if _, err := pgdb.Migrate(context.Background(), pool, dup, nil); err == nil || !strings.Contains(err.Error(), "pgdb: migrations") {
		t.Fatalf("duplicate versions: err = %v", err)
	}
}
