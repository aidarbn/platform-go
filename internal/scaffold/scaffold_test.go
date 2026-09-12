package scaffold_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/scaffold"
	"github.com/aidarbn/platform-go/internal/spec"
)

func TestNewRequiresModulePath(t *testing.T) {
	_, err := scaffold.New(scaffold.Options{Dir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "Go module path") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewRejectsUnknownModule(t *testing.T) {
	_, err := scaffold.New(scaffold.Options{Dir: t.TempDir(), Module: "example.com/app", Modules: []string{"redis"}})
	if err == nil || !strings.Contains(err.Error(), "unknown module") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewRejectsNonEmptyDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := scaffold.New(scaffold.Options{Dir: dir, Module: "example.com/app"})
	if err == nil || !strings.Contains(err.Error(), "is not empty") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewCreatesProject(t *testing.T) {
	dir := t.TempDir()

	created, err := scaffold.New(scaffold.Options{
		Dir:     dir,
		Module:  "example.com/shop-api",
		Modules: []string{"postgres"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, want := range []string{
		spec.FileName, "go.mod", "cmd/app/main.go", "cmd/app/wire.go",
		"cmd/app/config.gen.go", "cmd/app/modules.gen.go", ".env.example", "Makefile", "README.md",
	} {
		if !contains(created, want) {
			t.Errorf("%s was not created (created: %v)", want, created)
		}
	}

	f, err := spec.Load(filepath.Join(dir, spec.FileName))
	if err != nil {
		t.Fatalf("project description cannot be read: %v", err)
	}
	if f.Service() != "shop-api" {
		t.Errorf("service = %q", f.Service())
	}
	if mods := f.EnabledModules(); len(mods) != 1 || mods[0].Name != "postgres" {
		t.Errorf("modules = %+v", mods)
	}
}

// Enabling the settings module gives the project a schema file and typed access to it.
func TestNewCreatesSettings(t *testing.T) {
	dir := t.TempDir()

	created, err := scaffold.New(scaffold.Options{
		Dir:     dir,
		Module:  "example.com/shop-api",
		Modules: []string{"postgres", "settings"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, want := range []string{"settings.yaml", "internal/settings/settings.gen.go"} {
		if !contains(created, want) {
			t.Errorf("%s was not created (created: %v)", want, created)
		}
	}

	code, err := os.ReadFile(filepath.Join(dir, "internal/settings/settings.gen.go"))
	if err != nil {
		t.Fatalf("read the generated file: %v", err)
	}
	if !strings.Contains(string(code), "func (g AppSettings) Maintenance() bool") {
		t.Errorf("the example schema did not reach the generated code:\n%s", code)
	}
}

// A created project must compile right away, without a single manual edit.
func TestNewProjectCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("building a project takes time")
	}

	dir := t.TempDir()
	if _, err := scaffold.New(scaffold.Options{
		Dir:     dir,
		Module:  "example.com/shop-api",
		Modules: []string{"postgres", "settings"},
		Require: "v0.0.0",
		Replace: repoRoot(t),
	}); err != nil {
		t.Fatalf("New: %v", err)
	}

	goRun(t, dir, "mod", "tidy")
	goRun(t, dir, "build", "./...")
	goRun(t, dir, "vet", "./...")

	cmd := exec.Command("gofmt", "-l", ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gofmt: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("files do not pass gofmt:\n%s", out)
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the repository path")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func goRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off", "GOPRIVATE=*")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}
