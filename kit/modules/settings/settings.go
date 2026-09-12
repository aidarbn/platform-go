// Package settings is a platform module: business settings of the project, stored in
// the database and editable without a deploy.
//
// The schema is generated from settings.yaml and comes from project code; the module
// adds storage, caching, refresh and the store in the container.
package settings

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/modules/postgres"
	"github.com/aidarbn/platform-go/kit/platform"
	"github.com/aidarbn/platform-go/kit/settingsx"
)

// DefaultTable is where overrides are stored.
const DefaultTable = "platform_settings"

// Config holds module settings. Load fills it from the environment; the platform
// generator writes the Load call into the project's config.gen.go.
type Config struct {
	Table string // table holding the overrides

	// RefreshInterval is how often values are re-read, so a change made on one instance
	// reaches the others. Zero disables the refresh loop.
	RefreshInterval time.Duration
}

// Load reads the module settings from environment variables.
func Load(l *confx.Loader) Config {
	return Config{
		Table:           l.String("SETTINGS_TABLE", DefaultTable),
		RefreshInterval: l.Duration("SETTINGS_REFRESH_INTERVAL", 15*time.Second),
	}
}

// Module implements platform.Module.
type Module struct {
	cfg    Config
	schema settingsx.Schema
	store  *settingsx.Store

	done chan struct{}
	wg   sync.WaitGroup

	mu         sync.Mutex
	lastErr    error
	reloadFail prometheus.Counter
}

// New creates the module from ready settings and the project schema.
func New(cfg Config, schema settingsx.Schema) *Module {
	if cfg.Table == "" {
		cfg.Table = DefaultTable
	}
	return &Module{cfg: cfg, schema: schema, done: make(chan struct{})}
}

func (m *Module) Name() string { return "settings" }

// Init creates the table, loads the values and puts the store into the container,
// where project code picks it up through the generated accessors.
func (m *Module) Init(ctx context.Context, app *platform.App) error {
	if err := validTable(m.cfg.Table); err != nil {
		return err
	}

	pool, ok := platform.Lookup[*pgxpool.Pool](app)
	if !ok {
		return errors.New("no database in the container: enable the postgres module")
	}
	if err := ensureTable(ctx, pool, m.cfg.Table); err != nil {
		return err
	}

	m.store = settingsx.NewStore(m.schema, &repo{pool: pool, table: m.cfg.Table}, app.Logger())
	if err := m.store.Reload(ctx); err != nil {
		return err
	}
	platform.Provide(app, m.store)

	return m.registerMetrics(app)
}

func (m *Module) registerMetrics(app *platform.App) error {
	m.reloadFail = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "settings",
		Name:      "reload_failures_total",
		Help:      "failed attempts to re-read the settings",
	})
	overrides := prometheus.NewGaugeFunc(
		prometheus.GaugeOpts{
			Namespace: "settings",
			Name:      "overrides",
			Help:      "settings that differ from their defaults",
		},
		func() float64 { return float64(m.store.Overrides()) },
	)
	for _, c := range []prometheus.Collector{m.reloadFail, overrides} {
		if err := app.Metrics().Register(c); err != nil {
			return fmt.Errorf("settings metrics: %w", err)
		}
	}
	return nil
}

// Start begins the refresh loop: a value changed on one instance takes effect on the
// others within RefreshInterval, with no restart and no deploy.
func (m *Module) Start(ctx context.Context) error {
	if m.cfg.RefreshInterval <= 0 {
		return nil
	}

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(m.cfg.RefreshInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-m.done:
				return
			case <-ticker.C:
				m.refresh(ctx)
			}
		}
	}()
	return nil
}

func (m *Module) refresh(ctx context.Context) {
	err := m.store.Reload(ctx)

	m.mu.Lock()
	m.lastErr = err
	m.mu.Unlock()

	if err != nil && m.reloadFail != nil {
		m.reloadFail.Inc()
	}
}

// Health fails when the settings cannot be re-read: the application still works on the
// cached values, yet a change would not reach it, and that must be visible.
func (m *Module) Health(context.Context) error {
	if m.store == nil {
		return errors.New("settings are not loaded")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastErr
}

// Stop ends the refresh loop.
func (m *Module) Stop(context.Context) error {
	select {
	case <-m.done:
	default:
		close(m.done)
	}
	m.wg.Wait()
	return nil
}

// Store returns the store from the container.
func Store(app *platform.App) *settingsx.Store { return settingsx.From(app) }

// Pool is the database of the module, kept next to the settings table.
var _ = postgres.Pool

// repo stores the overrides in PostgreSQL.
type repo struct {
	pool  *pgxpool.Pool
	table string
}

func (r *repo) Load(ctx context.Context) (map[string]string, error) {
	rows, err := r.pool.Query(ctx, "SELECT key, value FROM "+r.table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, rows.Err()
}

func (r *repo) Save(ctx context.Context, key, value, actor string) error {
	_, err := r.pool.Exec(ctx,
		"INSERT INTO "+r.table+" (key, value, updated_by) VALUES ($1, $2, $3) "+
			"ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_by = excluded.updated_by, updated_at = now()",
		key, value, actor)
	return err
}

func (r *repo) Delete(ctx context.Context, key string) error {
	_, err := r.pool.Exec(ctx, "DELETE FROM "+r.table+" WHERE key = $1", key)
	return err
}

// ensureTable creates the table on the first run: overrides are platform state, not
// project data, so the module owns the table instead of the project migrations.
func ensureTable(ctx context.Context, pool *pgxpool.Pool, table string) error {
	_, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS `+table+` (
	key        text        PRIMARY KEY,
	value      text        NOT NULL,
	updated_by text        NOT NULL DEFAULT '',
	updated_at timestamptz NOT NULL DEFAULT now()
)`)
	if err != nil {
		return fmt.Errorf("settings: create table %s: %w", table, err)
	}
	return nil
}

// validTable guards the table name: it goes into SQL as text, so anything but a plain
// identifier is refused rather than escaped.
func validTable(table string) error {
	parts := strings.Split(table, ".")
	if len(parts) > 2 {
		return fmt.Errorf("settings: table %q: expected name or schema.name", table)
	}
	for _, part := range parts {
		if !identifier(part) {
			return fmt.Errorf("settings: table %q: only lower case letters, digits and underscore are allowed", table)
		}
	}
	return nil
}

func identifier(s string) bool {
	if s == "" || (s[0] != '_' && (s[0] < 'a' || s[0] > 'z')) {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '_':
		default:
			return false
		}
	}
	return true
}
