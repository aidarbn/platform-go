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

	pgxdecimal "github.com/jackc/pgx-shopspring-decimal"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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

	// Hooks are what a project adds to every connection and to the pool settings; nil adds
	// nothing. They sit behind a pointer, so Config stays comparable with ==.
	Hooks *Hooks
}

// Hooks extend the pool Open makes.
type Hooks struct {
	// AfterConnect runs on every new connection, after the platform has registered its
	// types (numeric as decimal.Decimal). Projects register their own types here, such as
	// enums or composite types. An error discards the connection.
	AfterConnect func(ctx context.Context, conn *pgx.Conn) error

	// ConfigurePool adjusts the pgx pool settings once the fields of Config are applied,
	// for what Config does not cover: BeforeAcquire, a tracer, runtime parameters. An
	// AfterConnect set here runs after the platform types and before Hooks.AfterConnect.
	ConfigurePool func(*pgxpool.Config)
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
	var hooks Hooks
	if cfg.Hooks != nil {
		hooks = *cfg.Hooks
	}
	if hooks.ConfigurePool != nil {
		hooks.ConfigurePool(poolCfg)
	}
	poolCfg.AfterConnect = afterConnect(poolCfg.AfterConnect, hooks.AfterConnect)

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

// RegisterTypes registers the types the platform maps for every project on a connection:
// numeric as decimal.Decimal, the type the generated sqlc config uses for it. The codec
// sends numeric in the binary format, also inside arrays, instead of the text fallback
// through sql.Scanner. Open calls
// it on every connection; call it yourself on a connection made without Open.
func RegisterTypes(conn *pgx.Conn) {
	m := conn.TypeMap()
	pgxdecimal.Register(m)
	// Scanning into decimal.Decimal goes through the scan plans Register adds; its codec
	// would also turn numeric scanned into any — rows.Values, pgx.RowToMap — into
	// decimal.Decimal, which JSON writes as a string and which has no NaN or Infinity.
	// The codec of pgx keeps those as pgtype.Numeric, as without the platform types.
	m.RegisterType(&pgtype.Type{Name: "numeric", OID: pgtype.NumericOID, Codec: pgtype.NumericCodec{}})
}

// afterConnect chains the platform types with the hooks of the project, in order. A
// failing hook stops the chain, and pgx discards the connection.
func afterConnect(hooks ...func(context.Context, *pgx.Conn) error) func(context.Context, *pgx.Conn) error {
	return func(ctx context.Context, conn *pgx.Conn) error {
		RegisterTypes(conn)
		for _, hook := range hooks {
			if hook == nil {
				continue
			}
			if err := hook(ctx, conn); err != nil {
				return err
			}
		}
		return nil
	}
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
