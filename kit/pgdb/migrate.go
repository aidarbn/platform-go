package pgdb

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// Migrate applies the pending SQL migrations from fsys and returns the files it applied.
//
// Migrations are goose files ("-- +goose Up"). A session advisory lock is held while they
// run, so several instances starting at once apply each migration exactly once. Files
// other than *.sql are ignored: the embedded directory also holds its Go file.
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, log *slog.Logger) ([]string, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	sqlFiles := sqlOnly{fsys}

	names, err := fs.Glob(sqlFiles, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("pgdb: migrations: %w", err)
	}
	if len(names) == 0 {
		return nil, nil
	}

	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, fmt.Errorf("pgdb: migrations lock: %w", err)
	}

	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()

	provider, err := goose.NewProvider(goose.DialectPostgres, db, sqlFiles, goose.WithSessionLocker(locker))
	if errors.Is(err, goose.ErrNoMigrations) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pgdb: migrations: %w", err)
	}

	results, err := provider.Up(ctx)
	applied := make([]string, 0, len(results))
	for _, r := range results {
		if r.Error == nil && r.Source != nil {
			applied = append(applied, path.Base(r.Source.Path))
			log.Info("migration applied", "file", path.Base(r.Source.Path), "duration", r.Duration)
		}
	}
	if err != nil {
		return applied, fmt.Errorf("pgdb: migrations: %w", err)
	}
	return applied, nil
}

// sqlOnly hides everything but *.sql files from goose.
type sqlOnly struct{ fs.FS }

func (s sqlOnly) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(s.FS, name)
	if err != nil {
		return nil, err
	}
	out := entries[:0:0]
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			out = append(out, e)
		}
	}
	return out, nil
}
