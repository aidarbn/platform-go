package gomod_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/gomod"
)

func project(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAddAndDropTool(t *testing.T) {
	dir := project(t, "module example.com/app\n\ngo 1.27\n")
	if !gomod.Exists(dir) || gomod.Exists(t.TempDir()) {
		t.Fatal("Exists is wrong")
	}

	if err := gomod.AddTool(dir, "github.com/sqlc-dev/sqlc", "github.com/sqlc-dev/sqlc/cmd/sqlc", "v1.31.1"); err != nil {
		t.Fatalf("AddTool: %v", err)
	}
	f, err := gomod.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if f.Module != "example.com/app" || !f.HasTool("github.com/sqlc-dev/sqlc/cmd/sqlc") || f.Requires["github.com/sqlc-dev/sqlc"] != "v1.31.1" {
		t.Errorf("go.mod = %+v", f)
	}

	if err := gomod.DropTool(dir, "github.com/sqlc-dev/sqlc/cmd/sqlc"); err != nil {
		t.Fatalf("DropTool: %v", err)
	}
	f, err = gomod.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if f.HasTool("github.com/sqlc-dev/sqlc/cmd/sqlc") {
		t.Errorf("the tool is still declared: %+v", f)
	}
}

// A version the project has chosen itself is not downgraded to the platform's pin.
func TestAddToolKeepsProjectVersion(t *testing.T) {
	dir := project(t, "module example.com/app\n\ngo 1.27\n\nrequire github.com/sqlc-dev/sqlc v1.99.0\n")
	if err := gomod.AddTool(dir, "github.com/sqlc-dev/sqlc", "github.com/sqlc-dev/sqlc/cmd/sqlc", "v1.31.1"); err != nil {
		t.Fatalf("AddTool: %v", err)
	}
	f, err := gomod.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if f.Requires["github.com/sqlc-dev/sqlc"] != "v1.99.0" {
		t.Errorf("version = %s", f.Requires["github.com/sqlc-dev/sqlc"])
	}
}

func TestReadBrokenGoMod(t *testing.T) {
	dir := project(t, "this is not go.mod\n")
	if _, err := gomod.Read(dir); err == nil || !strings.Contains(err.Error(), "go mod edit") {
		t.Fatalf("err = %v", err)
	}
}

func TestLocalReplace(t *testing.T) {
	dir := project(t, "module example.com/app\n\ngo 1.27\n\nreplace (\n\texample.com/lib => ../lib\n\texample.com/old v1.0.0 => ./old\n\texample.com/fork => example.com/fork2 v1.2.0\n)\n")
	f, err := gomod.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Replaced) != 3 || len(f.Local) != 2 || f.Local[0] != (gomod.LocalReplace{Path: "example.com/lib", Dir: "../lib"}) ||
		f.Local[1] != (gomod.LocalReplace{Path: "example.com/old", Version: "v1.0.0", Dir: "./old"}) {
		t.Fatalf("replaced = %v, local = %+v", f.Replaced, f.Local)
	}
	for _, r := range f.Local {
		r.Dir = "/abs/" + r.Path
		if err := gomod.SetLocalReplace(dir, r); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
	for _, want := range []string{"example.com/lib => /abs/example.com/lib", "example.com/old v1.0.0 => /abs/example.com/old"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("go.mod lacks %q:\n%s", want, raw)
		}
	}
}
