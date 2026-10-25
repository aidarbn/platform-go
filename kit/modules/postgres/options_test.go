package postgres_test

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/modules/postgres"
	"github.com/aidarbn/platform-go/kit/platform"
)

// Taply's sizes, the example the defaults of an application were made for.
var taply = postgres.PoolSizes{MaxConns: 25, MinConns: 5, MaxConnIdleTime: 10 * time.Minute}

func load(t *testing.T) postgres.Config {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://localhost/app")
	l := confx.New("")
	cfg := postgres.Load(l)
	if err := l.Err(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestWithoutDefaultsKeepsPlatformValues(t *testing.T) {
	got := postgres.New(load(t)).PoolConfig()
	if got.MaxConns != 10 || got.MinConns != 0 || got.MaxConnLifetime != time.Hour ||
		got.MaxConnIdleTime != 30*time.Minute || got.ConnectTimeout != 5*time.Second {
		t.Errorf("pool = %+v", got)
	}
	if got.Hooks != nil {
		t.Error("no hooks were given, none must be passed on")
	}
}

func TestDefaultsReplacePlatformValues(t *testing.T) {
	got := postgres.New(load(t), postgres.WithDefaults(taply)).PoolConfig()
	if got.MaxConns != 25 || got.MinConns != 5 || got.MaxConnIdleTime != 10*time.Minute {
		t.Errorf("pool = %+v", got)
	}
	// Fields the application left zero keep the platform defaults.
	if got.MaxConnLifetime != time.Hour || got.ConnectTimeout != 5*time.Second {
		t.Errorf("pool = %+v", got)
	}
}

func TestEnvironmentBeatsDefaults(t *testing.T) {
	t.Setenv("DATABASE_MAX_CONNS", "40")
	t.Setenv("DATABASE_MIN_CONNS", "0") // zero set explicitly still wins over the 5 of the application
	t.Setenv("DATABASE_MAX_CONN_IDLE_TIME", "1m")
	t.Setenv("DATABASE_MAX_CONN_LIFETIME", "2h")
	t.Setenv("DATABASE_CONNECT_TIMEOUT", "3s")

	got := postgres.New(load(t), postgres.WithDefaults(postgres.PoolSizes{
		MaxConns: 25, MinConns: 5, MaxConnLifetime: 30 * time.Minute,
		MaxConnIdleTime: 10 * time.Minute, ConnectTimeout: time.Second,
	})).PoolConfig()
	if got.MaxConns != 40 || got.MinConns != 0 || got.MaxConnIdleTime != time.Minute ||
		got.MaxConnLifetime != 2*time.Hour || got.ConnectTimeout != 3*time.Second {
		t.Errorf("pool = %+v", got)
	}
}

// A Config built by hand has no environment to consult: its non-zero fields count as set.
func TestHandBuiltConfigBeatsDefaults(t *testing.T) {
	cfg := postgres.Config{URL: "postgres://localhost/app", MaxConns: 7}
	got := postgres.New(cfg, postgres.WithDefaults(taply)).PoolConfig()
	if got.MaxConns != 7 || got.MinConns != 5 || got.MaxConnIdleTime != 10*time.Minute {
		t.Errorf("pool = %+v", got)
	}
	// Zero fields without a default of the application stay zero for pgdb to fill.
	if got.MaxConnLifetime != 0 || got.URL != cfg.URL {
		t.Errorf("pool = %+v", got)
	}
}

// A project may change a size in main.go after Load; that change is explicit too.
func TestChangeAfterLoadBeatsDefaults(t *testing.T) {
	cfg := load(t)
	cfg.MaxConns = 50
	cfg.MinConns = 0 // unchanged from Load, so the default of the application applies
	got := postgres.New(cfg, postgres.WithDefaults(taply)).PoolConfig()
	if got.MaxConns != 50 || got.MinConns != 5 {
		t.Errorf("pool = %+v", got)
	}
}

func TestNilOptionsAreIgnored(t *testing.T) {
	got := postgres.New(load(t), postgres.WithAfterConnect(nil), postgres.WithPoolConfig(nil)).PoolConfig()
	if got.Hooks != nil {
		t.Error("nil hooks must not be passed on")
	}
}

func TestLaterDefaultsOverrideEarlier(t *testing.T) {
	got := postgres.New(load(t),
		postgres.WithDefaults(taply),
		postgres.WithDefaults(postgres.PoolSizes{MaxConns: 50}),
	).PoolConfig()
	if got.MaxConns != 50 || got.MinConns != 5 {
		t.Errorf("pool = %+v", got)
	}
}

func TestApply(t *testing.T) {
	m, other := postgres.New(load(t)), postgres.New(load(t))
	modules := []platform.Module{fakeModule{}, m, other}
	if err := postgres.Apply(modules, postgres.WithDefaults(taply)); err != nil {
		t.Fatal(err)
	}
	if a, b := m.PoolConfig(), other.PoolConfig(); a.MaxConns != 25 || b.MaxConns != 25 {
		t.Errorf("Apply did not reach every module: %+v, %+v", a, b)
	}

	err := postgres.Apply([]platform.Module{fakeModule{}}, postgres.WithDefaults(taply))
	if err == nil || !strings.Contains(err.Error(), "postgres module is not among") {
		t.Fatalf("err = %v", err)
	}
}

func TestHooksChainInOrder(t *testing.T) {
	var order []string
	hook := func(name string, err error) func(context.Context, *pgx.Conn) error {
		return func(context.Context, *pgx.Conn) error { order = append(order, name); return err }
	}
	cfg := postgres.New(load(t),
		postgres.WithAfterConnect(hook("first", nil)),
		postgres.WithAfterConnect(hook("second", context.Canceled)),
		postgres.WithAfterConnect(hook("third", nil)),
		postgres.WithPoolConfig(func(c *pgxpool.Config) { order = append(order, "pool-a") }),
		postgres.WithPoolConfig(func(c *pgxpool.Config) { order = append(order, "pool-b") }),
	).PoolConfig()

	if err := cfg.Hooks.AfterConnect(context.Background(), nil); err != context.Canceled {
		t.Errorf("err = %v: the error of a hook must stop the chain and come back", err)
	}
	cfg.Hooks.ConfigurePool(&pgxpool.Config{})
	if strings.Join(order, ",") != "first,second,pool-a,pool-b" {
		t.Errorf("order = %v", order)
	}
}

type fakeModule struct{}

func (fakeModule) Name() string                              { return "fake" }
func (fakeModule) Init(context.Context, *platform.App) error { return nil }
func (fakeModule) Health(context.Context) error              { return nil }
func (fakeModule) Stop(context.Context) error                { return nil }

// Against a real database: the pool the module opens has the sizes of the application
// where the environment is silent, the hooks run on every connection, and numeric reads
// as decimal.Decimal through the pool from the container.
func TestOptionsWithRealDatabase(t *testing.T) {
	url := os.Getenv("DATABASE_TEST_URL")
	if url == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}
	t.Setenv("DATABASE_URL", url)
	t.Setenv("DATABASE_MAX_CONNS", "6") // the environment beats the 25 of the application
	t.Setenv("DATABASE_MIGRATE", "false")
	l := confx.New("")
	cfg := postgres.Load(l)
	if err := l.Err(); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	m := postgres.New(cfg)
	modules := []platform.Module{m}
	if err := postgres.Apply(modules,
		postgres.WithDefaults(taply),
		postgres.WithAfterConnect(func(context.Context, *pgx.Conn) error { calls.Add(1); return nil }),
		postgres.WithPoolConfig(func(c *pgxpool.Config) { c.ConnConfig.RuntimeParams["application_name"] = "pr3-module" }),
	); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	app := platform.NewApp(nil)
	if err := m.Init(ctx, app); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(ctx)
	pool := postgres.Pool(app)

	pc := pool.Config()
	if pc.MaxConns != 6 || pc.MinConns != 5 || pc.MaxConnIdleTime != 10*time.Minute {
		t.Errorf("pool = max %d min %d idle %s", pc.MaxConns, pc.MinConns, pc.MaxConnIdleTime)
	}

	var (
		amount decimal.Decimal
		name   string
	)
	if err := pool.QueryRow(ctx, `select $1::numeric * 2, current_setting('application_name')`,
		decimal.RequireFromString("1234.5678")).Scan(&amount, &name); err != nil {
		t.Fatal(err)
	}
	if !amount.Equal(decimal.RequireFromString("2469.1356")) || name != "pr3-module" {
		t.Errorf("amount = %s, application_name = %q", amount, name)
	}

	// MinConns makes the pool open connections in the background; wait until none is
	// still being built, since a connection counts before its hooks have run.
	deadline := time.Now().Add(5 * time.Second)
	for {
		st := pool.Stat()
		if st.TotalConns() >= 5 && st.ConstructingConns() == 0 && calls.Load() == st.TotalConns() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("hook calls = %d, connections = %d (%d being built)", calls.Load(), st.TotalConns(), st.ConstructingConns())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A Module declared without New takes options and opens with the platform values.
func TestZeroModuleTakesOptions(t *testing.T) {
	m := &postgres.Module{}
	if got := m.PoolConfig(); got.MaxConns != 0 || got.Hooks != nil {
		t.Errorf("pool = %+v", got)
	}
	if err := postgres.Apply([]platform.Module{m}, postgres.WithDefaults(taply)); err != nil {
		t.Fatal(err)
	}
	if got := m.PoolConfig(); got.MaxConns != 25 {
		t.Errorf("pool = %+v", got)
	}
}
