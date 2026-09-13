package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/gen"
	"github.com/aidarbn/platform-go/internal/spec"
)

func output(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	err := run(args, &buf)
	return buf.String(), err
}

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := output(t, args...)
	if err != nil {
		t.Fatalf("platformgo %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// The module path may stand before or after the flags: the help shows it last, and
// that is how it gets typed.
func TestNewAcceptsFlagsAfterModulePath(t *testing.T) {
	for name, args := range map[string][]string{
		"flags after":  {"new", "example.com/shop-api", "--with", "postgres,settings"},
		"flags before": {"new", "--with", "postgres,settings", "example.com/shop-api"},
		"equals form":  {"new", "example.com/shop-api", "--with=postgres,settings"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "project")
			out := mustRun(t, append(args, "--dir", dir)...)

			for _, want := range []string{spec.FileName, "settings.yaml", gen.SettingsPath, gen.ModulesPath} {
				if !strings.Contains(out, want) {
					t.Errorf("%s is missing from the output:\n%s", want, out)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, gen.SettingsPath)); err != nil {
				t.Errorf("the generated file is missing: %v", err)
			}
		})
	}
}

func TestNewRequiresModulePath(t *testing.T) {
	_, err := output(t, "new", "--dir", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "Go module path") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewRejectsExtraArguments(t *testing.T) {
	_, err := output(t, "new", "example.com/a", "example.com/b", "--dir", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unexpected arguments") {
		t.Fatalf("err = %v", err)
	}
}

func TestCommandsRejectExtraArguments(t *testing.T) {
	for _, cmd := range []string{"generate", "plan", "doctor"} {
		if _, err := output(t, cmd, "nonsense"); err == nil || !strings.Contains(err.Error(), "unexpected arguments") {
			t.Errorf("%s: err = %v", cmd, err)
		}
	}
}

// What a fresh project contains must be exactly what generation produces, otherwise a
// project would fail its own CI right after being created.
func TestGeneratedProjectIsUpToDate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	mustRun(t, "new", "example.com/shop-api", "--with", "postgres,settings", "--dir", dir)

	if out := mustRun(t, "generate", "--check", "--dir", dir); !strings.Contains(out, "up to date") {
		t.Errorf("generate --check:\n%s", out)
	}
	if out := mustRun(t, "plan", "--dir", dir); !strings.Contains(out, "no changes") {
		t.Errorf("plan:\n%s", out)
	}
}

// Editing the schema is the whole point: plan shows it, --check fails in CI until
// generate is run, and the new setting reaches the generated code.
func TestSchemaChangeFlowsThroughGeneration(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	mustRun(t, "new", "example.com/shop-api", "--with", "postgres,settings", "--dir", dir)

	schema := filepath.Join(dir, "settings.yaml")
	raw, err := os.ReadFile(schema)
	if err != nil {
		t.Fatal(err)
	}
	added := string(raw) + "  api.ratelimit:\n    rps: { type: int, default: 50, min: 1 }\n"
	if err := os.WriteFile(schema, []byte(added), 0o600); err != nil {
		t.Fatal(err)
	}

	if out := mustRun(t, "plan", "--dir", dir); !strings.Contains(out, gen.SettingsPath) {
		t.Errorf("plan does not mention the settings:\n%s", out)
	}
	if _, err := output(t, "generate", "--check", "--dir", dir); err == nil ||
		!strings.Contains(err.Error(), "generation is stale") {
		t.Fatalf("err = %v", err)
	}

	mustRun(t, "generate", "--dir", dir)
	code, err := os.ReadFile(filepath.Join(dir, gen.SettingsPath))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(code), "func (g ApiRatelimitSettings) Rps() int") {
		t.Errorf("the new setting did not reach the generated code:\n%s", code)
	}
	if out := mustRun(t, "generate", "--check", "--dir", dir); !strings.Contains(out, "up to date") {
		t.Errorf("generate --check:\n%s", out)
	}
}

// A mistake in the schema must stop generation and name the place.
func TestGenerateReportsBadSchema(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	mustRun(t, "new", "example.com/shop-api", "--with", "postgres,settings", "--dir", dir)

	if err := os.WriteFile(filepath.Join(dir, "settings.yaml"),
		[]byte("settings:\n  api:\n    rps: { type: int, default: many }\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := output(t, "generate", "--dir", dir)
	if err == nil || !strings.Contains(err.Error(), "api.rps") {
		t.Fatalf("err = %v", err)
	}
}

func TestPlanListsModules(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	mustRun(t, "new", "example.com/shop-api", "--with", "postgres,settings", "--dir", dir)

	out := mustRun(t, "plan", "--dir", dir)
	for _, want := range []string{"shop-api", "postgres, settings"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan lacks %q:\n%s", want, out)
		}
	}
}

func TestGenerateOutsideProject(t *testing.T) {
	_, err := output(t, "generate", "--dir", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), spec.FileName) {
		t.Fatalf("err = %v", err)
	}
}

func TestUnknownCommand(t *testing.T) {
	out, err := output(t, "deploy")
	if err == nil || !strings.Contains(err.Error(), `unknown command "deploy"`) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "platformgo new") {
		t.Errorf("an unknown command must print the usage:\n%s", out)
	}
}

func TestUsageAndVersion(t *testing.T) {
	for _, args := range [][]string{{}, {"help"}, {"--help"}} {
		if out := mustRun(t, args...); !strings.Contains(out, "platformgo generate") {
			t.Errorf("%v:\n%s", args, out)
		}
	}
	if out := mustRun(t, "version"); strings.TrimSpace(out) == "" {
		t.Error("version printed nothing")
	}
}

// Removing a module is a one line edit of platformgo.yaml: CI catches the leftover until
// apply runs, and apply takes the module's generated code away.
func TestRemovingModuleThroughApply(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	mustRun(t, "new", "example.com/shop-api", "--with", "postgres,settings,admin", "--dir", dir)

	description := filepath.Join(dir, spec.FileName)
	raw, err := os.ReadFile(description)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(raw), "  settings: {}\n", "", 1)
	if edited == string(raw) {
		t.Fatalf("the settings module is not in the description:\n%s", raw)
	}
	if err := os.WriteFile(description, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}

	out := mustRun(t, "plan", "--dir", dir)
	for _, want := range []string{"- module settings", "- " + gen.SettingsPath} {
		if !strings.Contains(out, want) {
			t.Errorf("plan lacks %q:\n%s", want, out)
		}
	}

	if _, err := output(t, "verify", "--dir", dir); err == nil || !strings.Contains(err.Error(), gen.SettingsPath) {
		t.Fatalf("verify: %v", err)
	}

	out = mustRun(t, "apply", "--no-tidy", "--dir", dir)
	if !strings.Contains(out, "deleted "+gen.SettingsPath) {
		t.Errorf("apply:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, gen.SettingsPath)); !os.IsNotExist(err) {
		t.Errorf("the generated settings code is still there: %v", err)
	}
	if out := mustRun(t, "verify", "--dir", dir); !strings.Contains(out, "up to date") {
		t.Errorf("verify after apply:\n%s", out)
	}
}

func TestNewWritesLock(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	out := mustRun(t, "new", "example.com/shop-api", "--with", "postgres", "--dir", dir)
	if !strings.Contains(out, "platformgo.lock") {
		t.Errorf("new did not report the lock:\n%s", out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "platformgo.lock"))
	if err != nil {
		t.Fatalf("no lock: %v", err)
	}
	if !strings.Contains(string(raw), "postgres") || !strings.Contains(string(raw), gen.ModulesPath) {
		t.Errorf("lock:\n%s", raw)
	}
}

func TestMigrateCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	mustRun(t, "new", "example.com/shop-api", "--with", "postgres", "--dir", dir)

	out := mustRun(t, "migrate", "create", "create orders", "--dir", dir)
	if !strings.Contains(out, "created db/migrations/") || !strings.Contains(out, "_create_orders.sql") {
		t.Fatalf("output:\n%s", out)
	}
	// A new SQL file changes nothing that is generated.
	if out := mustRun(t, "verify", "--dir", dir); !strings.Contains(out, "up to date") {
		t.Errorf("verify:\n%s", out)
	}

	if _, err := output(t, "migrate", "create", "--dir", dir); err == nil {
		t.Error("a migration without a name was accepted")
	}
	if _, err := output(t, "migrate", "status"); err == nil {
		t.Error("an unknown migrate command was accepted")
	}
	if _, err := output(t, "migrate", "create", "x", "--dir", t.TempDir()); err == nil ||
		!strings.Contains(err.Error(), "not a platformgo project") {
		t.Errorf("outside a project: %v", err)
	}
}
