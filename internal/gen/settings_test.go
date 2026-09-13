package gen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/gen"
)

const withSettings = `
schema: 1
project:
  module: github.com/aidarbn/shop-api
  service: shop-api
modules:
  postgres: {}
  settings: {}
`

const schemaYAML = `
settings:
  orders.cleanup:
    enabled:  { type: bool, default: true }
    schedule: { type: cron, default: "0 3 * * *" }
  orders.create:
    max_attempts: { type: int, default: 5, min: 1, max: 20 }
    timeout:      { type: duration, default: 30s }
    mode:         { type: string, default: fast, options: [fast, slow] }
`

func TestSettingsCode(t *testing.T) {
	code, err := gen.SettingsCode([]byte(schemaYAML))
	if err != nil {
		t.Fatalf("SettingsCode: %v", err)
	}

	got := string(code)
	for _, want := range []string{
		"package settings",
		`settingsx.Definition{Key: "orders.create.max_attempts", Group: "orders.create", Name: "max_attempts", Kind: settingsx.KindInt, Default: "5", Min: "1", Max: "20"}`,
		`settingsx.Definition{Key: "orders.create.mode", Group: "orders.create", Name: "mode", Kind: settingsx.KindString, Default: "fast", Options: []string{"fast", "slow"}}`,
		"func From(app *platform.App) *Settings",
		"func (s *Settings) OrdersCleanup() OrdersCleanupSettings",
		`func (g OrdersCleanupSettings) Schedule() string { return g.store.Cron("orders.cleanup.schedule") }`,
		`func (g OrdersCleanupSettings) Enabled() bool { return g.store.Bool("orders.cleanup.enabled") }`,
		`func (g OrdersCreateSettings) MaxAttempts() int { return g.store.Int("orders.create.max_attempts") }`,
		"func (g OrdersCreateSettings) Timeout() time.Duration {",
		`return g.store.Duration("orders.create.timeout")`,
		`"time"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("settings.gen.go lacks %q:\n%s", want, got)
		}
	}
}

// An unused import does not compile, so time appears only when a duration setting does.
func TestSettingsCodeWithoutDurations(t *testing.T) {
	code, err := gen.SettingsCode([]byte("settings:\n  app:\n    maintenance: { type: bool, default: false }\n"))
	if err != nil {
		t.Fatalf("SettingsCode: %v", err)
	}
	if got := string(code); strings.Contains(got, `"time"`) {
		t.Errorf("time must not be imported:\n%s", got)
	}
}

func TestSettingsCodeEmptySchema(t *testing.T) {
	code, err := gen.SettingsCode([]byte("settings: {}\n"))
	if err != nil {
		t.Fatalf("SettingsCode: %v", err)
	}
	if got := string(code); !strings.Contains(got, "var Schema = settingsx.MustSchema()") {
		t.Errorf("an empty schema must still generate:\n%s", got)
	}
}

// Two groups whose names differ only in punctuation would give one Go type: that is a
// schema mistake and must be reported, not generated into broken code.
func TestSettingsCodeRejectsNameClash(t *testing.T) {
	_, err := gen.SettingsCode([]byte(
		"settings:\n  a.b:\n    x: { type: bool, default: true }\n  a_b:\n    y: { type: bool, default: true }\n"))
	if err == nil || !strings.Contains(err.Error(), "same name AB") {
		t.Fatalf("err = %v", err)
	}
}

func TestSettingsCodeRejectsSettingNameClash(t *testing.T) {
	_, err := gen.SettingsCode([]byte(
		"settings:\n  a:\n    max_attempts: { type: int, default: 1 }\n    maxAttempts: { type: int, default: 1 }\n"))
	if err == nil || !strings.Contains(err.Error(), "same name MaxAttempts") {
		t.Fatalf("err = %v", err)
	}
}

// The module needs the schema from the project, so the wiring imports the generated
// package under an alias: its package name is the same as the module's.
func TestModulesFileWiresSettingsSchema(t *testing.T) {
	files, err := gen.Wiring(mustParse(t, withSettings))
	if err != nil {
		t.Fatalf("Wiring: %v", err)
	}

	got := string(files[gen.ModulesPath])
	for _, want := range []string{
		`appsettings "github.com/aidarbn/shop-api/internal/settings"`,
		"settings.New(cfg.Settings, appsettings.Schema)",
		"postgres.New(cfg.Postgres, postgres.WithMigrations(migrations.FS)),\n\t\tsettings.New(",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("modules.gen.go lacks %q:\n%s", want, got)
		}
	}

	// The settings type of the module is enough for config.gen.go: the project package
	// would be an unused import there.
	if cfg := string(files[gen.ConfigPath]); strings.Contains(cfg, "appsettings") {
		t.Errorf("config.gen.go must not import the project package:\n%s", cfg)
	}
}

func TestFilesGeneratesSettings(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.yaml"), []byte(schemaYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	files, err := gen.Files(dir, mustParse(t, withSettings))
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	for _, want := range []string{gen.ConfigPath, gen.ModulesPath, gen.EnvPath, gen.SettingsPath} {
		if _, ok := files[want]; !ok {
			t.Errorf("%s is missing: %v", want, keys(files))
		}
	}

	// Without the settings module nothing extra is generated.
	plain, err := gen.Files(dir, mustParse(t, withPostgres))
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if _, ok := plain[gen.SettingsPath]; ok {
		t.Error("settings are generated without the module being enabled")
	}
}

func TestFilesReportsMissingSchemaFile(t *testing.T) {
	_, err := gen.Files(t.TempDir(), mustParse(t, withSettings))
	if err == nil || !strings.Contains(err.Error(), "settings.yaml") {
		t.Fatalf("err = %v", err)
	}
}

// The schema file may live elsewhere; the module section says where.
func TestFilesHonoursSchemaPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config", "business.yaml"), []byte(schemaYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	f := mustParse(t, strings.Replace(withSettings, "settings: {}", "settings:\n    schema: config/business.yaml", 1))
	if got := gen.SettingsSchemaPath(f); got != "config/business.yaml" {
		t.Fatalf("SettingsSchemaPath = %q", got)
	}
	if _, err := gen.Files(dir, f); err != nil {
		t.Fatalf("Files: %v", err)
	}
}

func TestSettingsCodeDeterministic(t *testing.T) {
	first, err := gen.SettingsCode([]byte(schemaYAML))
	if err != nil {
		t.Fatalf("SettingsCode: %v", err)
	}
	for range 5 {
		next, err := gen.SettingsCode([]byte(schemaYAML))
		if err != nil {
			t.Fatalf("SettingsCode: %v", err)
		}
		if string(next) != string(first) {
			t.Fatal("settings.gen.go changes between runs")
		}
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestSettingsCodeTaplyTypesAndDescriptions(t *testing.T) {
	code, err := gen.SettingsCode([]byte(`configs:
  payments:
    _description: "Payments"
    max_amount:
      type: int64
      default: 5000000000
      description: "Largest payment, tiyn"
      requires_restart: true
    fee_percent: { type: float, default: 0.95 }
`))
	if err != nil {
		t.Fatalf("SettingsCode: %v", err)
	}
	got := string(code)
	for _, want := range []string{
		`func (g PaymentsSettings) MaxAmount() int64 { return g.store.Int64("payments.max_amount") }`,
		`func (g PaymentsSettings) FeePercent() float64 { return g.store.Float("payments.fee_percent") }`,
		`// MaxAmount returns "payments.max_amount". Largest payment, tiyn`,
		`Description: "Largest payment, tiyn", GroupDescription: "Payments", RequiresRestart: true`,
		"settingsx.KindInt64", "settingsx.KindFloat",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("settings.gen.go lacks %q:\n%s", want, got)
		}
	}
}
