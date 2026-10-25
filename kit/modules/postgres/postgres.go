// Package postgres is a platform module: a PostgreSQL connection pool with a health
// check, pool metrics and a clean shutdown.
//
// The module writes no queries: sqlc generates the static ones, jet builds the dynamic ones.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
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

	// Migrate applies pending migrations during startup. Turn it off when migrations
	// run as a separate step of the deploy.
	Migrate bool

	// fromEnv marks the pool sizes Load found in the environment, and loaded keeps what
	// Load returned. A size set in the environment or changed after Load beats the
	// defaults of the application (WithDefaults); the rest give way to them.
	fromEnv poolField
	loaded  PoolSizes
}

// poolField is a set of pool settings.
type poolField uint8

const (
	fieldMaxConns poolField = 1 << iota
	fieldMinConns
	fieldMaxConnLifetime
	fieldMaxConnIdleTime
	fieldConnectTimeout
	fieldLoaded // the Config came from Load, so the bits above are meaningful
)

// Load reads the module settings from environment variables.
func Load(l *confx.Loader) Config {
	cfg := Config{
		URL:             l.Required("DATABASE_URL"),
		MaxConns:        int32(l.Int("DATABASE_MAX_CONNS", 10)),
		MinConns:        int32(l.Int("DATABASE_MIN_CONNS", 0)),
		MaxConnLifetime: l.Duration("DATABASE_MAX_CONN_LIFETIME", time.Hour),
		MaxConnIdleTime: l.Duration("DATABASE_MAX_CONN_IDLE_TIME", 30*time.Minute),
		ConnectTimeout:  l.Duration("DATABASE_CONNECT_TIMEOUT", 5*time.Second),
		Migrate:         l.Bool("DATABASE_MIGRATE", true),
	}
	cfg.fromEnv = fieldLoaded
	for key, field := range map[string]poolField{
		"DATABASE_MAX_CONNS":          fieldMaxConns,
		"DATABASE_MIN_CONNS":          fieldMinConns,
		"DATABASE_MAX_CONN_LIFETIME":  fieldMaxConnLifetime,
		"DATABASE_MAX_CONN_IDLE_TIME": fieldMaxConnIdleTime,
		"DATABASE_CONNECT_TIMEOUT":    fieldConnectTimeout,
	} {
		if l.IsSet(key) {
			cfg.fromEnv |= field
		}
	}
	cfg.loaded = cfg.sizes()
	return cfg
}

// sizes returns the pool settings of the config.
func (c Config) sizes() PoolSizes {
	return PoolSizes{
		MaxConns:        c.MaxConns,
		MinConns:        c.MinConns,
		MaxConnLifetime: c.MaxConnLifetime,
		MaxConnIdleTime: c.MaxConnIdleTime,
		ConnectTimeout:  c.ConnectTimeout,
	}
}

// PoolSizes holds the pool settings an application chooses as its defaults. A zero field
// keeps the default of the platform, so a default cannot be zero; a project that needs
// a zero sets the field of Config or the DATABASE_* variable.
type PoolSizes struct {
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	ConnectTimeout  time.Duration
}

// Module implements platform.Module.
type Module struct {
	cfg        Config
	pool       *pgxpool.Pool
	db         *sql.DB
	migrations fs.FS
	opts       *poolOptions // behind a pointer, so Module stays comparable
}

// poolOptions holds what the project adds to the pool through options.
type poolOptions struct {
	defaults     PoolSizes
	afterConnect []func(context.Context, *pgx.Conn) error
	configure    []func(*pgxpool.Config)
}

// Option configures the module.
type Option func(*Module)

// WithMigrations gives the module the migrations of the project. The generated wiring
// passes the embedded db/migrations directory.
func WithMigrations(fsys fs.FS) Option {
	return func(m *Module) { m.migrations = fsys }
}

// WithAfterConnect adds a hook run on every new connection, after the platform has
// registered its types (numeric as decimal.Decimal). Projects register their own types
// here. Hooks run in the order they were added; an error discards the connection. A nil
// hook is ignored.
func WithAfterConnect(hook func(ctx context.Context, conn *pgx.Conn) error) Option {
	return func(m *Module) {
		if hook != nil {
			o := m.options()
			o.afterConnect = append(o.afterConnect, hook)
		}
	}
}

// WithPoolConfig adds a function that adjusts the pgx pool settings once the sizes are
// applied, for what the module does not expose. Functions run in the order they were
// added. A nil function is ignored.
func WithPoolConfig(fn func(*pgxpool.Config)) Option {
	return func(m *Module) {
		if fn != nil {
			o := m.options()
			o.configure = append(o.configure, fn)
		}
	}
}

// WithDefaults sets the pool sizes of the application. They replace the defaults of the
// platform, while a DATABASE_* variable that is set still wins, and so does a field of
// Config the project changed after Load. In a Config built by hand rather than by Load, a
// non-zero field counts as set. A zero field of p changes
// nothing; a later WithDefaults overrides the non-zero fields of an earlier one.
func WithDefaults(p PoolSizes) Option {
	return func(m *Module) {
		d := &m.options().defaults
		d.MaxConns = pick(p.MaxConns, d.MaxConns)
		d.MinConns = pick(p.MinConns, d.MinConns)
		d.MaxConnLifetime = pick(p.MaxConnLifetime, d.MaxConnLifetime)
		d.MaxConnIdleTime = pick(p.MaxConnIdleTime, d.MaxConnIdleTime)
		d.ConnectTimeout = pick(p.ConnectTimeout, d.ConnectTimeout)
	}
}

// pick returns v unless it is zero.
func pick[T comparable](v, otherwise T) T {
	var zero T
	if v == zero {
		return otherwise
	}
	return v
}

// New creates the module from ready settings.
func New(cfg Config, opts ...Option) *Module {
	m := &Module{cfg: cfg}
	m.apply(opts)
	return m
}

// options returns the pool options, creating them for a Module not built by New.
func (m *Module) options() *poolOptions {
	if m.opts == nil {
		m.opts = &poolOptions{}
	}
	return m.opts
}

func (m *Module) apply(opts []Option) {
	for _, opt := range opts {
		opt(m)
	}
}

// Apply passes options to the postgres modules among modules; call it before
// platform.Run, since the module opens its pool in Init. The module is built by the
// generated modules.gen.go, so a project adds its hooks and pool defaults in main.go:
//
//	modules := platformModules(cfg)
//	if err := postgres.Apply(modules,
//		postgres.WithDefaults(postgres.PoolSizes{MaxConns: 25, MinConns: 5, MaxConnIdleTime: 10 * time.Minute}),
//		postgres.WithAfterConnect(registerTypes),
//	); err != nil {
//		log.Fatal(err)
//	}
//
// Apply fails when modules has no postgres module: hooks the project relies on must not
// be dropped silently.
func Apply(modules []platform.Module, opts ...Option) error {
	found := false
	for _, mod := range modules {
		if m, ok := mod.(*Module); ok {
			m.apply(opts)
			found = true
		}
	}
	if !found {
		return errors.New("postgres: the postgres module is not among the modules")
	}
	return nil
}

// poolConfig returns the pgdb settings. For every size the first of these wins: a
// value set explicitly (for a Config from Load a DATABASE_* variable or a change made
// after Load, for one built by hand a non-zero field), the default of the application,
// the value in Config.
func (m *Module) poolConfig() pgdb.Config {
	explicit := func(field poolField, zero, changed bool) bool {
		if m.cfg.fromEnv&fieldLoaded != 0 {
			return m.cfg.fromEnv&field != 0 || changed
		}
		return !zero
	}
	c, l, o := m.cfg, m.cfg.loaded, m.options()
	d, hooks, configure := o.defaults, o.afterConnect, o.configure
	cfg := pgdb.Config{
		URL:             c.URL,
		MaxConns:        resolve(c.MaxConns, d.MaxConns, explicit(fieldMaxConns, c.MaxConns == 0, c.MaxConns != l.MaxConns)),
		MinConns:        resolve(c.MinConns, d.MinConns, explicit(fieldMinConns, c.MinConns == 0, c.MinConns != l.MinConns)),
		MaxConnLifetime: resolve(c.MaxConnLifetime, d.MaxConnLifetime, explicit(fieldMaxConnLifetime, c.MaxConnLifetime == 0, c.MaxConnLifetime != l.MaxConnLifetime)),
		MaxConnIdleTime: resolve(c.MaxConnIdleTime, d.MaxConnIdleTime, explicit(fieldMaxConnIdleTime, c.MaxConnIdleTime == 0, c.MaxConnIdleTime != l.MaxConnIdleTime)),
		ConnectTimeout:  resolve(c.ConnectTimeout, d.ConnectTimeout, explicit(fieldConnectTimeout, c.ConnectTimeout == 0, c.ConnectTimeout != l.ConnectTimeout)),
	}
	if len(hooks) > 0 {
		cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
			for _, hook := range hooks {
				if err := hook(ctx, conn); err != nil {
					return err
				}
			}
			return nil
		}
	}
	if len(configure) > 0 {
		cfg.ConfigurePool = func(pc *pgxpool.Config) {
			for _, fn := range configure {
				fn(pc)
			}
		}
	}
	return cfg
}

// resolve keeps an explicit value, otherwise takes the default of the application when
// there is one.
func resolve[T comparable](value, appDefault T, explicit bool) T {
	var zero T
	if explicit || appDefault == zero {
		return value
	}
	return appDefault
}

func (m *Module) Name() string { return "postgres" }

// Init opens the pool, verifies the connection and puts the pool into the container,
// where other modules and project code pick it up through Pool(app).
func (m *Module) Init(ctx context.Context, app *platform.App) error {
	pool, err := pgdb.Open(ctx, m.poolConfig())
	if err != nil {
		return err
	}
	m.pool = pool

	// Migrations run before anything else touches the database, so every module and the
	// domain start on the current schema.
	if m.migrations != nil && m.cfg.Migrate {
		if _, err := pgdb.Migrate(ctx, pool, m.migrations, app.Logger()); err != nil {
			return err
		}
	}

	// database/sql over the same pool, for libraries built on it, such as the jet query
	// runner. It shares the pool's connections instead of opening its own.
	m.db = stdlib.OpenDBFromPool(pool)

	platform.Provide(app, pool)
	platform.Provide(app, m.db)
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
	if m.db != nil {
		_ = m.db.Close() // closing it leaves the pool open; the pool is closed next
	}
	if m.pool != nil {
		m.pool.Close()
	}
	return nil
}

// Pool returns the pool from the container.
func Pool(app *platform.App) *pgxpool.Pool { return platform.Get[*pgxpool.Pool](app) }

// SQL returns database/sql over the pool from the container. Dynamic queries built with
// jet run through it: stmt.QueryContext(ctx, postgres.SQL(app), &dest).
func SQL(app *platform.App) *sql.DB { return platform.Get[*sql.DB](app) }

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
