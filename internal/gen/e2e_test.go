package gen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/gen"
	"github.com/aidarbn/platform-go/internal/spec"
)

// Сквозная проверка: в пустом каталоге создаётся проект, генерируется подключение
// модулей, и всё это компилируется. Проект содержит только предметную часть —
// main.go и сборку домена; остальное сгенерировано.
func TestGeneratedProjectCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("сборка проекта занимает время")
	}

	root := repoRoot(t)
	dir := t.TempDir()

	write(t, dir, spec.FileName, `schema: 1
platform: v0.1.0
project:
  module: example.com/app
  service: shop-api
modules:
  postgres: {}
`)

	write(t, dir, "go.mod", `module example.com/app

go 1.27

require github.com/aidarbn/platform-go v0.0.0

replace github.com/aidarbn/platform-go => `+root+`
`)

	// Единственные написанные руками файлы проекта: точка входа и сборка домена.
	write(t, dir, "cmd/app/main.go", `package main

import (
	"log"

	"github.com/aidarbn/platform-go/kit/platform"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatal(err)
	}
	if err := platform.Run(platform.Config{Service: "shop-api"}, platformModules(cfg), wireDomain); err != nil {
		log.Fatal(err)
	}
}
`)

	write(t, dir, "cmd/app/wire.go", `package main

import (
	"github.com/aidarbn/platform-go/kit/modules/postgres"
	"github.com/aidarbn/platform-go/kit/platform"
)

// wireDomain — предметная часть проекта.
func wireDomain(app *platform.App) error {
	_ = postgres.Pool(app) // репозитории проекта берут пул из контейнера
	return nil
}
`)

	f, err := spec.Load(filepath.Join(dir, spec.FileName))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	files, err := gen.Wiring(f)
	if err != nil {
		t.Fatalf("Wiring: %v", err)
	}
	if _, err := gen.Apply(dir, files); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	goCmd(t, dir, "mod", "tidy")
	goCmd(t, dir, "build", "./...")
	goCmd(t, dir, "vet", "./...")

	// Сгенерированное должно проходить gofmt: в CI проекта это проверяется.
	if unformatted := gofmtList(t, dir); unformatted != "" {
		t.Errorf("gofmt нашёл неотформатированные файлы:\n%s", unformatted)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("не удалось определить путь к репозиторию")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func write(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func goCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out := goOutput(t, dir, args...); strings.TrimSpace(out) != "" {
		t.Logf("go %s: %s", strings.Join(args, " "), out)
	}
}

func goOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GOFLAGS=-mod=mod",
		"GOPROXY=off", // собираем из кэша модулей, без сети
		"GOPRIVATE=*", // без обращения к базе контрольных сумм
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// gofmtList возвращает файлы, которые не проходят gofmt.
func gofmtList(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("gofmt", "-l", ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gofmt: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}
