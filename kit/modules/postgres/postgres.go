// Package postgres is a platform module: a PostgreSQL connection pool with a health
// check, pool metrics and a clean shutdown.
//
// The module writes no queries: sqlc generates the static ones, jet builds the dynamic ones.
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/pgdb"
	"github.com/aidarbn/platform-go/kit/platform"
)

// Config holds module settings. Load fills it from the environment; the platform
// generator writes the Load call into the project's config.gen.go.
type Config struct {
	URL             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	ConnectTimeout  time.Duration
}

// Load reads the module settings from environment variables.
func Load(l *confx.Loader) Config {
	return Config{
		URL:             l.Required("DATABASE_URL"),
		MaxConns:        int32(l.Int("DATABASE_MAX_CONNS", 10)),
		MinConns:        int32(l.Int("DATABASE_MIN_CONNS", 0)),
		MaxConnLifetime: l.Duration("DATABASE_MAX_CONN_LIFETIME", time.Hour),
		MaxConnIdleTime: l.Duration("DATABASE_MAX_CONN_IDLE_TIME", 30*time.Minute),
		ConnectTimeout:  l.Duration("DATABASE_CONNECT_TIMEOUT", 5*time.Second),
	}
}

// Module implements platform.Module.
type Module struct {
	cfg  Config
	pool *pgxpool.Pool
}

// New creates the module from ready settings.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

func (m *Module) Name() string { return "postgres" }

// Init opens the pool, verifies the connection and puts the pool into the container,
// where other modules and project code pick it up through Pool(app).
func (m *Module) Init(ctx context.Context, app *platform.App) error {
	pool, err := pgdb.Open(ctx, pgdb.Config{
		URL:             m.cfg.URL,
		MaxConns:        m.cfg.MaxConns,
		MinConns:        m.cfg.MinConns,
		MaxConnLifetime: m.cfg.MaxConnLifetime,
		MaxConnIdleTime: m.cfg.MaxConnIdleTime,
		ConnectTimeout:  m.cfg.ConnectTimeout,
	})
	if err != nil {
		return err
	}
	m.pool = pool

	platform.Provide(app, pool)
	if err := app.Metrics().Register(newPoolCollector(pool)); err != nil {
		return fmt.Errorf("pool metrics: %w", err)
	}
	return nil
}

// Health reports whether the database answers.
func (m *Module) Health(ctx context.Context) error {
	if m.pool == nil {
		return fmt.Errorf("pool is not created")
	}
	return m.pool.Ping(ctx)
}

// Stop closes the pool, waiting for busy connections to come back.
func (m *Module) Stop(context.Context) error {
	if m.pool != nil {
		m.pool.Close()
	}
	return nil
}

// Pool returns the pool from the container.
func Pool(app *platform.App) *pgxpool.Pool { return platform.Get[*pgxpool.Pool](app) }

// InTx runs fn in a transaction on the pool from the container.
func InTx(ctx context.Context, app *platform.App, fn func(tx pgx.Tx) error) error {
	return pgdb.InTx(ctx, Pool(app), fn)
}

// newPoolCollector exposes pool state in /metrics: these numbers show connection
// starvation before it turns into timeouts for clients.
func newPoolCollector(pool *pgxpool.Pool) prometheus.Collector {
	gauge := func(name, help string, value func(*pgxpool.Stat) float64) prometheus.Collector {
		return prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{Namespace: "pgdb", Name: name, Help: help},
			func() float64 { return value(pool.Stat()) },
		)
	}
	return collectors{
		gauge("pool_total_conns", "connections in the pool", func(s *pgxpool.Stat) float64 { return float64(s.TotalConns()) }),
		gauge("pool_acquired_conns", "connections in use", func(s *pgxpool.Stat) float64 { return float64(s.AcquiredConns()) }),
		gauge("pool_idle_conns", "idle connections", func(s *pgxpool.Stat) float64 { return float64(s.IdleConns()) }),
		gauge("pool_max_conns", "connection limit", func(s *pgxpool.Stat) float64 { return float64(s.MaxConns()) }),
	}
}

// collectors groups several metrics into one collector.
type collectors []prometheus.Collector

func (c collectors) Describe(ch chan<- *prometheus.Desc) {
	for _, m := range c {
		m.Describe(ch)
	}
}

func (c collectors) Collect(ch chan<- prometheus.Metric) {
	for _, m := range c {
		m.Collect(ch)
	}
}
