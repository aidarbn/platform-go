package gen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/gen"
	"github.com/aidarbn/platform-go/internal/spec"
)

const withPostgres = `
schema: 1
project:
  module: github.com/aidarbn/shop-api
  service: shop-api
modules:
  postgres: {}
`

func mustParse(t *testing.T, raw string) *spec.File {
	t.Helper()
	f, err := spec.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return f
}

func TestModulesFileExact(t *testing.T) {
	files, err := gen.Wiring(mustParse(t, withPostgres))
	if err != nil {
		t.Fatalf("Wiring: %v", err)
	}

	want := `// Код сгенерирован platformgo по platformgo.yaml. Не правьте вручную.

package main

import (
	"github.com/aidarbn/platform-go/kit/modules/postgres"
	"github.com/aidarbn/platform-go/kit/platform"
)

// platformModules возвращает модули проекта в порядке зависимостей.
func platformModules(cfg *Config) []platform.Module {
	return []platform.Module{
		postgres.New(cfg.Postgres),
	}
}
`
	if got := string(files[gen.ModulesPath]); got != want {
		t.Errorf("modules.gen.go:\n--- получили ---\n%s\n--- ждали ---\n%s", got, want)
	}
}

func TestConfigFileContents(t *testing.T) {
	files, err := gen.Wiring(mustParse(t, withPostgres))
	if err != nil {
		t.Fatalf("Wiring: %v", err)
	}

	got := string(files[gen.ConfigPath])
	for _, want := range []string{
		"Postgres postgres.Config",
		"Postgres: postgres.Load(l)",
		"сервиса shop-api",
		`"github.com/aidarbn/platform-go/kit/confx"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("в config.gen.go нет %q:\n%s", want, got)
		}
	}
}

func TestEnvExample(t *testing.T) {
	files, err := gen.Wiring(mustParse(t, withPostgres))
	if err != nil {
		t.Fatalf("Wiring: %v", err)
	}

	got := string(files[gen.EnvPath])
	for _, want := range []string{"# модуль postgres", "DATABASE_URL=postgres://", "(обязательно)"} {
		if !strings.Contains(got, want) {
			t.Errorf("в .env.example нет %q:\n%s", want, got)
		}
	}
}

func TestNoModulesStillValid(t *testing.T) {
	files, err := gen.Wiring(mustParse(t, "schema: 1\nproject:\n  module: github.com/x/app\n"))
	if err != nil {
		t.Fatalf("Wiring: %v", err)
	}
	if got := string(files[gen.ModulesPath]); !strings.Contains(got, "return []platform.Module{}") {
		t.Errorf("без модулей ждали пустой список:\n%s", got)
	}
}

func TestDeterministic(t *testing.T) {
	f := mustParse(t, withPostgres)

	first, err := gen.Wiring(f)
	if err != nil {
		t.Fatalf("Wiring: %v", err)
	}
	second, err := gen.Wiring(f)
	if err != nil {
		t.Fatalf("Wiring: %v", err)
	}
	for path := range first {
		if string(first[path]) != string(second[path]) {
			t.Errorf("%s меняется между запусками", path)
		}
	}
}

func TestApplyAndChanged(t *testing.T) {
	dir := t.TempDir()
	files, err := gen.Wiring(mustParse(t, withPostgres))
	if err != nil {
		t.Fatalf("Wiring: %v", err)
	}

	changed, err := gen.Changed(dir, files)
	if err != nil {
		t.Fatalf("Changed: %v", err)
	}
	if len(changed) != len(files) {
		t.Errorf("в пустом проекте все файлы должны считаться изменёнными: %v", changed)
	}

	written, err := gen.Apply(dir, files)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(written) != len(files) {
		t.Errorf("Apply записал %v", written)
	}

	changed, err = gen.Changed(dir, files)
	if err != nil {
		t.Fatalf("Changed: %v", err)
	}
	if len(changed) != 0 {
		t.Errorf("после Apply расхождений быть не должно: %v", changed)
	}

	// Ручная правка сгенерированного файла обнаруживается.
	if err := os.WriteFile(filepath.Join(dir, gen.ModulesPath), []byte("// правка руками\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err = gen.Changed(dir, files)
	if err != nil {
		t.Fatalf("Changed: %v", err)
	}
	if len(changed) != 1 || changed[0] != gen.ModulesPath {
		t.Errorf("ждали расхождение в %s, получили %v", gen.ModulesPath, changed)
	}
}
