package spec_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/spec"
)

const minimal = `
schema: 1
platform: v0.1.0
project:
  module: github.com/aidarbn/shop-api
  service: shop-api
modules:
  postgres:
    migrations: db/migrations
`

func TestParseMinimal(t *testing.T) {
	f, err := spec.Parse([]byte(minimal))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Project.Module != "github.com/aidarbn/shop-api" || f.Service() != "shop-api" {
		t.Errorf("project = %+v", f.Project)
	}
	mods := f.EnabledModules()
	if len(mods) != 1 || mods[0].Name != "postgres" {
		t.Errorf("modules = %+v", mods)
	}
}

func TestServiceFromModulePath(t *testing.T) {
	f, err := spec.Parse([]byte("schema: 1\nproject:\n  module: github.com/aidarbn/shop-api\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := f.Service(); got != "shop-api" {
		t.Errorf("Service = %q", got)
	}
}

func TestRequiresSchema(t *testing.T) {
	_, err := spec.Parse([]byte("project:\n  module: github.com/x/y\n"))
	if err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("err = %v", err)
	}
}

func TestRejectsNewerSchema(t *testing.T) {
	_, err := spec.Parse([]byte("schema: 99\nproject:\n  module: github.com/x/y\n"))
	if err == nil || !strings.Contains(err.Error(), "upgrade platformgo") {
		t.Fatalf("err = %v", err)
	}
}

func TestRequiresModulePath(t *testing.T) {
	_, err := spec.Parse([]byte("schema: 1\n"))
	if err == nil || !strings.Contains(err.Error(), "project.module") {
		t.Fatalf("err = %v", err)
	}
}

func TestRejectsUnknownModule(t *testing.T) {
	_, err := spec.Parse([]byte("schema: 1\nproject:\n  module: github.com/x/y\nmodules:\n  redis: {}\n"))
	if err == nil || !strings.Contains(err.Error(), "unknown module") {
		t.Fatalf("err = %v", err)
	}
}

func TestRejectsUnknownField(t *testing.T) {
	_, err := spec.Parse([]byte("schema: 1\nproject:\n  modul: github.com/x/y\n"))
	if err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("a typo in a field name must be an error, got %v", err)
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, spec.FileName)
	if err := os.WriteFile(path, []byte(minimal), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := spec.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f.Platform != "v0.1.0" {
		t.Errorf("platform = %q", f.Platform)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := spec.Load(filepath.Join(t.TempDir(), spec.FileName))
	if err == nil || !strings.Contains(err.Error(), spec.FileName) {
		t.Fatalf("err = %v", err)
	}
}
