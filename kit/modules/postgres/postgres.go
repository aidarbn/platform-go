// Package postgres — модуль платформы: пул соединений к PostgreSQL, проверка health,
// метрики пула и корректное закрытие при остановке.
//
// Запросы модуль не пишет: статические генерирует sqlc, динамические собирает jet.
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

// Config — настройки модуля. Заполняется Load из переменных окружения;
// генератор платформы кладёт вызов Load в config.gen.go проекта.
type Config struct {
	URL             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	ConnectTimeout  time.Duration
}

// Load читает настройки модуля из окружения.
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

// Module реализует platform.Module.
type Module struct {
	cfg  Config
	pool *pgxpool.Pool
}

// New создаёт модуль с готовыми настройками.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

func (m *Module) Name() string { return "postgres" }

// Init открывает пул, проверяет соединение и кладёт пул в контейнер:
// остальные модули и код проекта берут его через Pool(app).
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
		return fmt.Errorf("метрики пула: %w", err)
	}
	return nil
}

// Health проверяет, что база отвечает.
func (m *Module) Health(ctx context.Context) error {
	if m.pool == nil {
		return fmt.Errorf("пул не создан")
	}
	return m.pool.Ping(ctx)
}

// Stop закрывает пул, дожидаясь возврата занятых соединений.
func (m *Module) Stop(context.Context) error {
	if m.pool != nil {
		m.pool.Close()
	}
	return nil
}

// Pool возвращает пул из контейнера.
func Pool(app *platform.App) *pgxpool.Pool { return platform.Get[*pgxpool.Pool](app) }

// InTx выполняет fn в транзакции на пуле из контейнера.
func InTx(ctx context.Context, app *platform.App, fn func(tx pgx.Tx) error) error {
	return pgdb.InTx(ctx, Pool(app), fn)
}

// newPoolCollector отдаёт состояние пула в /metrics: по этим числам видно
// исчерпание соединений раньше, чем оно превратится в таймауты у клиентов.
func newPoolCollector(pool *pgxpool.Pool) prometheus.Collector {
	gauge := func(name, help string, value func(*pgxpool.Stat) float64) prometheus.Collector {
		return prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{Namespace: "pgdb", Name: name, Help: help},
			func() float64 { return value(pool.Stat()) },
		)
	}
	return collectors{
		gauge("pool_total_conns", "всего соединений в пуле", func(s *pgxpool.Stat) float64 { return float64(s.TotalConns()) }),
		gauge("pool_acquired_conns", "занятых соединений", func(s *pgxpool.Stat) float64 { return float64(s.AcquiredConns()) }),
		gauge("pool_idle_conns", "свободных соединений", func(s *pgxpool.Stat) float64 { return float64(s.IdleConns()) }),
		gauge("pool_max_conns", "предел соединений", func(s *pgxpool.Stat) float64 { return float64(s.MaxConns()) }),
	}
}

// collectors объединяет несколько метрик в один коллектор.
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
