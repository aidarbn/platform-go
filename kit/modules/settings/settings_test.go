package settings_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/logx"
	"github.com/aidarbn/platform-go/kit/modules/postgres"
	"github.com/aidarbn/platform-go/kit/modules/settings"
	"github.com/aidarbn/platform-go/kit/pgdb"
	"github.com/aidarbn/platform-go/kit/platform"
	"github.com/aidarbn/platform-go/kit/settingsx"
)

func schema(t *testing.T) settingsx.Schema {
	t.Helper()
	s, err := settingsx.NewSchema(
		settingsx.Definition{Key: "api.ratelimit.rps", Group: "api.ratelimit", Name: "rps", Kind: settingsx.KindInt, Default: "50", Min: "1"},
		settingsx.Definition{Key: "app.maintenance", Group: "app", Name: "maintenance", Kind: settingsx.KindBool, Default: "false"},
	)
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}
	return s
}

func TestLoadDefaults(t *testing.T) {
	l := confx.New("")
	cfg := settings.Load(l)

	if err := l.Err(); err != nil {
		t.Fatalf("err = %v", err)
	}
	if cfg.Table != settings.DefaultTable || cfg.RefreshInterval != 15*time.Second {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestLoadReadsValues(t *testing.T) {
	t.Setenv("SETTINGS_TABLE", "app_settings")
	t.Setenv("SETTINGS_REFRESH_INTERVAL", "1s")

	l := confx.New("")
	cfg := settings.Load(l)

	if err := l.Err(); err != nil {
		t.Fatalf("err = %v", err)
	}
	if cfg.Table != "app_settings" || cfg.RefreshInterval != time.Second {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestInitNeedsDatabase(t *testing.T) {
	err := settings.New(settings.Config{}, schema(t)).Init(context.Background(), platform.NewApp(nil))
	if err == nil || !strings.Contains(err.Error(), "enable the postgres module") {
		t.Fatalf("err = %v", err)
	}
}

// The table name goes into SQL as text, so anything but a plain identifier is refused.
func TestInitRejectsBadTableName(t *testing.T) {
	for _, table := range []string{"app settings", `settings"; drop table users; --`, "a.b.c", "1settings", "Settings"} {
		err := settings.New(settings.Config{Table: table}, schema(t)).Init(context.Background(), platform.NewApp(nil))
		if err == nil || !strings.Contains(err.Error(), "settings: table") {
			t.Errorf("table %q: err = %v", table, err)
		}
	}
}

func TestHealthWithoutInit(t *testing.T) {
	if err := settings.New(settings.Config{}, schema(t)).Health(context.Background()); err == nil {
		t.Fatal("health must fail before Init")
	}
}

func TestStopIsSafeWithoutStart(t *testing.T) {
	m := settings.New(settings.Config{}, schema(t))
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

// Full lifecycle against a real database. Runs when DATABASE_TEST_URL is set: the
// platform boots postgres and settings, a value is changed and read back, and the
// refresh loop picks up a change made behind the store's back.
func TestLifecycleWithRealDatabase(t *testing.T) {
	url := os.Getenv("DATABASE_TEST_URL")
	if url == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	table := "platform_settings_test"

	// The test owns its table and drops it before and after through its own pool: the
	// application pool is closed by the time cleanup runs, so a leftover value would
	// otherwise break the next run.
	own, err := pgdb.Open(context.Background(), pgdb.Config{URL: url})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	dropTable := func() {
		if _, err := own.Exec(context.Background(), "DROP TABLE IF EXISTS "+table); err != nil {
			t.Errorf("drop %s: %v", table, err)
		}
	}
	dropTable()
	t.Cleanup(func() {
		dropTable()
		own.Close()
	})
	addrCh := make(chan string, 1)
	errCh := make(chan error, 1)
	storeCh := make(chan *settingsx.Store, 1)
	poolCh := make(chan *pgxpool.Pool, 1)

	cfg := platform.Config{
		Service:         "test",
		OpsAddr:         "127.0.0.1:0",
		ShutdownTimeout: 5 * time.Second,
		Logger:          logx.New(logx.Options{Writer: io.Discard}),
		OnStarted:       func(addr string) { addrCh <- addr },
	}
	modules := []platform.Module{
		postgres.New(postgres.Config{URL: url}),
		settings.New(settings.Config{Table: table, RefreshInterval: 200 * time.Millisecond}, schema(t)),
	}
	wire := func(app *platform.App) error {
		storeCh <- settings.Store(app)
		poolCh <- postgres.Pool(app)
		return nil
	}

	go func() { errCh <- platform.RunContext(ctx, cfg, modules, wire) }()

	var addr string
	select {
	case addr = <-addrCh:
	case err := <-errCh:
		t.Fatalf("Run returned before startup: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("the application did not start")
	}

	store, pool := <-storeCh, <-poolCh
	if got := store.Int("api.ratelimit.rps"); got != 50 {
		t.Errorf("rps = %d", got)
	}
	if err := store.Set(ctx, "api.ratelimit.rps", "120", "test"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := store.Int("api.ratelimit.rps"); got != 120 {
		t.Errorf("rps after Set = %d", got)
	}

	// The refresh loop must notice a change made straight in the database.
	changed := make(chan string, 4)
	store.Watch(func(keys []string) { changed <- strings.Join(keys, ",") })
	if _, err := pool.Exec(ctx,
		"UPDATE "+table+" SET value = '77' WHERE key = 'api.ratelimit.rps'"); err != nil {
		t.Fatalf("update: %v", err)
	}
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("the refresh loop did not notice the change")
	}
	if got := store.Int("api.ratelimit.rps"); got != 77 {
		t.Errorf("rps after refresh = %d", got)
	}

	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	defer resp.Body.Close()
	var body struct {
		Checks map[string]string `json:"checks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.StatusCode != http.StatusOK || body.Checks["settings"] != "ok" {
		t.Errorf("health = %d %+v", resp.StatusCode, body)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
}
