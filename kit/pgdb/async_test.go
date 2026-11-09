package pgdb_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aidarbn/platform-go/kit/pgdb"
)

// recordingTx records what was done to it and fails where told to.
type recordingTx struct {
	pgx.Tx
	execErr, commitErr error
	execs              []string
	committed, rolled  bool
}

func (t *recordingTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	t.execs = append(t.execs, sql)
	return pgconn.CommandTag{}, t.execErr
}
func (t *recordingTx) Commit(context.Context) error   { t.committed = true; return t.commitErr }
func (t *recordingTx) Rollback(context.Context) error { t.rolled = true; return nil }

type txBeginner struct {
	tx  *recordingTx
	err error
}

func (b txBeginner) Begin(context.Context) (pgx.Tx, error) { return b.tx, b.err }

func TestRunAsync(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	boom := errors.New("boom")
	setLocal := []string{"SET LOCAL synchronous_commit = off"}

	t.Run("SET LOCAL before the work, then commit", func(t *testing.T) {
		tx := &recordingTx{}
		seen := false
		err := pgdb.RunAsync(ctx, txBeginner{tx: tx}, func(got pgx.Tx) error { seen = got == tx; return nil })
		if err != nil || !seen || !tx.committed || !reflect.DeepEqual(tx.execs, setLocal) {
			t.Fatalf("err=%v seen=%v committed=%v execs=%v", err, seen, tx.committed, tx.execs)
		}
	})
	t.Run("Begin fails", func(t *testing.T) {
		if err := pgdb.RunAsync(ctx, txBeginner{err: boom}, func(pgx.Tx) error { return nil }); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("SET LOCAL fails: rolled back, the work is not run", func(t *testing.T) {
		tx := &recordingTx{execErr: boom}
		called := false
		err := pgdb.RunAsync(ctx, txBeginner{tx: tx}, func(pgx.Tx) error { called = true; return nil })
		if !errors.Is(err, boom) || called || !tx.rolled || tx.committed {
			t.Fatalf("err=%v called=%v rolled=%v committed=%v", err, called, tx.rolled, tx.committed)
		}
	})
	t.Run("the work fails: rolled back", func(t *testing.T) {
		tx := &recordingTx{}
		err := pgdb.RunAsync(ctx, txBeginner{tx: tx}, func(pgx.Tx) error { return boom })
		if !errors.Is(err, boom) || !tx.rolled || tx.committed {
			t.Fatalf("err=%v rolled=%v committed=%v", err, tx.rolled, tx.committed)
		}
	})
	t.Run("Commit fails", func(t *testing.T) {
		tx := &recordingTx{commitErr: boom}
		if err := pgdb.RunAsync(ctx, txBeginner{tx: tx}, func(pgx.Tx) error { return nil }); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	})
}
