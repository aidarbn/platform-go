package pgdb

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// AsyncCommit makes a transaction not wait for the WAL to reach the disk
// (synchronous_commit = off until its end), ported from taply's pgtx package.
//
// Why. On a host with a virtual disk fsync takes about 100 ms and every commit costs one
// to three seconds, so a heartbeat of a tablet answered in a second and a half for one
// UPDATE. With synchronous_commit = off Postgres confirms the commit at once and flushes
// in the background within wal_writer_delay (200 ms). What other sessions see does not
// change; only the last ~0.6 s of writes can be lost when the database server itself
// crashes.
//
// So only for writes such a loss does not harm: a heartbeat (the next comes in 15 s), a
// synchronisation (the next run rewrites everything). Orders, payments and edits of a
// menu stay synchronous.
func AsyncCommit(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SET LOCAL synchronous_commit = off")
	return err
}

// RunAsync runs fn in a transaction that does not wait for the disk (see AsyncCommit) and
// commits it; on any error the transaction is rolled back. db is a *pgxpool.Pool or a
// *pgx.Conn.
func RunAsync(ctx context.Context, db Beginner, fn func(tx pgx.Tx) error) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		// Its own context: the request one may already be cancelled, yet the connection
		// must return to the pool clean. After a commit the rollback is a no-op.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(ctx)
	}()
	if err := AsyncCommit(ctx, tx); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
