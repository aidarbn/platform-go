package settings_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aidarbn/platform-go/kit/modules/settings"
	"github.com/aidarbn/platform-go/kit/pgdb"
	"github.com/aidarbn/platform-go/kit/platform"
	"github.com/aidarbn/platform-go/kit/settingsx"
)

// appConfigs is taply's table: a row for every key written by EnsureDefaults, the value
// NULL when it means "use the default" (be-maas-api-go db/migrations 20251204150000,
// 20260127120000, 20260128170000).
const appConfigs = `CREATE TABLE app_configs_pr16 (
	key              text PRIMARY KEY,
	value            text,
	updated_at       timestamptz DEFAULT now(),
	updated_by       text,
	type             varchar(50),
	default_value    text,
	description      text,
	group_name       varchar(100),
	requires_restart boolean DEFAULT false
)`

func importSchema(t *testing.T) settingsx.Schema {
	t.Helper()
	s, err := settingsx.NewSchema(
		settingsx.Definition{Key: "sync.queue_workers", Group: "sync", Name: "queue_workers", Kind: settingsx.KindInt, Default: "3", Min: "1", Max: "50"},
		settingsx.Definition{Key: "sync.interval", Group: "sync", Name: "interval", Kind: settingsx.KindDuration, Default: "1m"},
		settingsx.Definition{Key: "sync.enabled", Group: "sync", Name: "enabled", Kind: settingsx.KindBool, Default: "true"},
		settingsx.Definition{Key: "payments.kaspi.fee_percent", Group: "payments.kaspi", Name: "fee_percent", Kind: settingsx.KindFloat, Default: "0.95"},
		settingsx.Definition{Key: "payments.kaspi.mode", Group: "payments.kaspi", Name: "mode", Kind: settingsx.KindString, Default: "test", Options: []string{"test", "live"}},
		settingsx.Definition{Key: "jobs.cleanup.schedule", Group: "jobs.cleanup", Name: "schedule", Kind: settingsx.KindCron, Default: "0 3 * * *"},
		settingsx.Definition{Key: "orders.max_total", Group: "orders", Name: "max_total", Kind: settingsx.KindInt64, Default: "1000000"},
	)
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}
	return s
}

// importApp initialises the settings module on its own table and returns the app, the
// pool of the app and a second pool that holds the source table.
func importApp(t *testing.T) (*platform.App, *pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("DATABASE_TEST_URL")
	if url == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}
	ctx := context.Background()
	const table = "platform_settings_import_test"

	own, err := pgdb.Open(ctx, pgdb.Config{URL: url})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	drop := func() {
		for _, q := range []string{"DROP TABLE IF EXISTS " + table, "DROP TABLE IF EXISTS app_configs_pr16"} {
			if _, err := own.Exec(ctx, q); err != nil {
				t.Errorf("%s: %v", q, err)
			}
		}
		_, _ = own.Exec(ctx, "DELETE FROM "+pgdb.SchemaVersionsTable+" WHERE owner = $1", "settings:"+table)
	}
	drop()
	t.Cleanup(func() {
		drop()
		own.Close()
	})
	if _, err := own.Exec(ctx, appConfigs); err != nil {
		t.Fatalf("create app_configs: %v", err)
	}

	pool, err := pgdb.Open(ctx, pgdb.Config{URL: url})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(pool.Close)

	app := platform.NewApp(nil)
	platform.Provide(app, pool)
	if err := settings.New(settings.Config{Table: table}, importSchema(t)).Init(ctx, app); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return app, pool, own
}

// The migration taply needs: app_configs holds a row for every key, mostly defaults
// written by EnsureDefaults, and only the real overrides reach platform_settings.
func TestImportQueryFromTaplyAppConfigs(t *testing.T) {
	app, pool, src := importApp(t)
	ctx := context.Background()

	if _, err := src.Exec(ctx, `INSERT INTO app_configs_pr16 (key, value, updated_by, type, default_value) VALUES
		('sync.queue_workers',         '3',          'system:init', 'int',      '3'),
		('sync.interval',              '1m0s',       'system:init', 'duration', '1m0s'),
		('sync.enabled',               'false',      'admin@taply', 'bool',     'true'),
		('payments.kaspi.fee_percent', '1.2',        'admin@taply', 'float',    '0.95'),
		('payments.kaspi.mode',        NULL,         NULL,          'string',   'test'),
		('jobs.cleanup.schedule',      '0 4 * * *',  'admin@taply', 'cron',     '0 3 * * *'),
		('orders.max_total',           'lots',       'admin@taply', 'int64',    '1000000'),
		('legacy.removed',             '42',         'system:init', 'int',      '42'),
		('legacy.removed_null',        NULL,         NULL,          'int',      NULL)`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const query = "SELECT key, value FROM app_configs_pr16"
	report, err := settings.ImportQuery(ctx, app, src, query, "import:app_configs")
	if err != nil {
		t.Fatalf("ImportQuery: %v", err)
	}
	t.Log(report.String())

	check := func(name string, got, want []string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	check("imported", report.Imported, []string{"jobs.cleanup.schedule", "payments.kaspi.fee_percent", "sync.enabled"})
	check("defaults", report.Defaults, []string{"payments.kaspi.mode", "sync.interval", "sync.queue_workers"})
	check("unknown", report.Unknown, []string{"legacy.removed", "legacy.removed_null"})
	if len(report.Rejected) != 1 || report.Rejected[0].Key != "orders.max_total" {
		t.Errorf("rejected = %+v", report.Rejected)
	}

	rows, err := pool.Query(ctx, "SELECT key, value, updated_by FROM platform_settings_import_test ORDER BY key")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	var got []string
	for rows.Next() {
		var key, value, by string
		if err := rows.Scan(&key, &value, &by); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, key+"="+value+" by "+by)
	}
	rows.Close()
	check("platform_settings", got, []string{
		"jobs.cleanup.schedule=0 4 * * * by import:app_configs",
		"payments.kaspi.fee_percent=1.2 by import:app_configs",
		"sync.enabled=false by import:app_configs",
	})

	store := settings.Store(app)
	if store.Bool("sync.enabled") || store.Float("payments.kaspi.fee_percent") != 1.2 {
		t.Error("the store does not read the imported values")
	}

	// A repeated run — taply calls it on every start until app_configs is dropped — stores
	// nothing new, and a value changed in the admin panel since then wins.
	if err := store.Set(ctx, "sync.enabled", "true", "admin"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	report, err = settings.ImportQuery(ctx, app, src, query, "import:app_configs")
	if err != nil {
		t.Fatalf("repeated ImportQuery: %v", err)
	}
	check("imported again", report.Imported, nil)
	check("unchanged", report.Unchanged, []string{"jobs.cleanup.schedule", "payments.kaspi.fee_percent"})
	check("kept", report.Kept, []string{"sync.enabled"})
	if !store.Bool("sync.enabled") {
		t.Error("the import overwrote the administrator's value")
	}
}

func TestImportWithRealDatabase(t *testing.T) {
	app, pool, _ := importApp(t)
	ctx := context.Background()

	report, err := settings.Import(ctx, app, map[string]string{"sync.interval": "90s", "sync.queue_workers": "03"}, "")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !slices.Equal(report.Imported, []string{"sync.interval"}) || !slices.Equal(report.Defaults, []string{"sync.queue_workers"}) {
		t.Errorf("report = %+v", report)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM platform_settings_import_test").Scan(&n); err != nil || n != 1 {
		t.Errorf("rows = %d, %v", n, err)
	}
}

func TestImportQueryReportsBadSources(t *testing.T) {
	app, _, src := importApp(t)
	ctx := context.Background()

	for _, c := range []struct {
		query string
		want  string
	}{
		{"SELECT key, value FROM no_such_table", "settings import: query"},
		{"SELECT 'sync.enabled'", "settings import: read"},
		{"SELECT 'sync.enabled', 'true' UNION ALL SELECT 'sync.enabled', NULL", `key "sync.enabled" appears twice`},
		{"SELECT 'a.b', (1/(g-2))::text FROM generate_series(1, 3) g", "settings import: read"},
	} {
		_, err := settings.ImportQuery(ctx, app, src, c.query, "import")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.query, err, c.want)
		}
	}
}

// The source is readable but the settings table is not: the error of the store comes
// back and nothing is reported as done.
func TestImportQueryReportsStoreErrors(t *testing.T) {
	app, pool, src := importApp(t)
	ctx := context.Background()
	pool.Close()

	report, err := settings.ImportQuery(ctx, app, src, "SELECT 'sync.enabled', 'false'", "import")
	if err == nil || !strings.Contains(err.Error(), "settings: load") {
		t.Fatalf("err = %v", err)
	}
	if report.Total() != 0 {
		t.Errorf("report = %+v", report)
	}
}
