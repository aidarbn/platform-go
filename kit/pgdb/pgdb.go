// Package pgdb opens a PostgreSQL connection pool and helps with transactions.
//
// It stays deliberately thin: queries are written by generators (sqlc for static
// ones, jet for dynamic ones) and only the parts identical in every project live here.
package pgdb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Config holds pool settings. Empty fields fall back to defaults.
type Config struct {
	URL             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	ConnectTimeout  time.Duration
}

func (c *Config) setDefaults() {
	if c.MaxConns == 0 {
		c.MaxConns = 10
	}
	if c.MaxConnLifetime == 0 {
		c.MaxConnLifetime = time.Hour
	}
	if c.MaxConnIdleTime == 0 {
		c.MaxConnIdleTime = 30 * time.Minute
	}
	if c.ConnectTimeout == 0 {
		c.ConnectTimeout = 5 * time.Second
	}
}

// Open creates the pool and verifies the connection. Without the check the service
// would start against an unreachable database and fail later, on a user request.
func Open(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	cfg.setDefaults()

	if cfg.URL == "" {
		return nil, errors.New("pgdb: database url is not set")
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("pgdb: parse database url: %w", err)
	}
	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("pgdb: create pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgdb: database is unreachable: %w", err)
	}
	return pool, nil
}

// InTx runs fn inside a transaction: it rolls back on error or panic and commits otherwise.
func InTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pgdb: begin transaction: %w", err)
	}

	committed := false
	defer func() {
		if committed {
			return
		}
		// Roll back with its own context: the request context may already be cancelled,
		// yet the connection must return to the pool clean.
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pgdb: commit transaction: %w", err)
	}
	committed = true
	return nil
}
