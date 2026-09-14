package apply_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/apply"
	"github.com/aidarbn/platform-go/internal/gen"
	"github.com/aidarbn/platform-go/internal/gomod"
	"github.com/aidarbn/platform-go/internal/lock"
	"github.com/aidarbn/platform-go/internal/spec"
)

const withSettings = `schema: 1
project:
  module: example.com/shop-api
modules:
  postgres: {}
  settings: {}
`

const withoutSettings = `schema: 1
project:
  module: example.com/shop-api
modules:
  postgres: {}
`

func project(t *testing.T, description string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, spec.FileName, description)
	return dir
}

func writeFile(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func build(t *testing.T, dir string) *apply.Plan {
	t.Helper()
	p, err := apply.Build(dir)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return p
}

func execute(t *testing.T, dir string) *apply.Plan {
	t.Helper()
	p := build(t, dir)
	if err := p.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return p
}

func exists(dir, path string) bool {
	_, err := os.Stat(filepath.Join(dir, path))
	return !errors.Is(err, os.ErrNotExist)
}

func TestFreshProject(t *testing.T) {
	dir := project(t, withSettings)

	p := build(t, dir)
	if p.UpToDate() {
		t.Fatal("a project never applied cannot be up to date")
	}
	// The settings module needs a schema, and the project gets an example one.
	if !slices.Equal(p.Create, []string{"settings.yaml"}) {
		t.Errorf("create = %v", p.Create)
	}
	for _, want := range []string{gen.ModulesPath, gen.SettingsPath, gen.ComposePath} {
		if !slices.Contains(p.Write, want) {
			t.Errorf("write lacks %s: %v", want, p.Write)
		}
	}
	if !p.LockStale || !slices.Equal(p.Diff.AddedModules, []string{"postgres", "settings"}) {
		t.Errorf("lock stale = %v, diff = %+v", p.LockStale, p.Diff)
	}
	// Building a plan changes nothing.
	if exists(dir, "settings.yaml") || exists(dir, lock.FileName) {
		t.Fatal("Build wrote files")
	}

	if err := p.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, path := range []string{"settings.yaml", gen.SettingsPath, lock.FileName} {
		if !exists(dir, path) {
			t.Errorf("%s is missing", path)
		}
	}
	if again := build(t, dir); !again.UpToDate() {
		t.Errorf("after Execute the plan is not empty: %v", again.Pending())
	}
}

// Taking a module out of platformgo.yaml removes its generated files, the folders they
// leave empty and the module from the lock — and keeps the files that belong to the
// project.
func TestRemovingModuleCleansUp(t *testing.T) {
	dir := project(t, withSettings)
	execute(t, dir)

	writeFile(t, dir, spec.FileName, withoutSettings)
	p := build(t, dir)
	if !slices.Equal(p.Delete, []string{gen.SettingsPath}) {
		t.Errorf("delete = %v", p.Delete)
	}
	if !slices.Equal(p.Diff.RemovedModules, []string{"settings"}) {
		t.Errorf("removed = %v", p.Diff.RemovedModules)
	}
	if err := p.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if exists(dir, gen.SettingsPath) {
		t.Error("the generated settings code is still there")
	}
	if exists(dir, "internal/settings") || exists(dir, "internal") {
		t.Error("empty folders were left behind")
	}
	if !exists(dir, "settings.yaml") {
		t.Error("the schema belongs to the project and must stay")
	}
	modules, err := os.ReadFile(filepath.Join(dir, gen.ModulesPath))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(modules), "settings") {
		t.Errorf("the wiring still has the module:\n%s", modules)
	}

	l, err := lock.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(l.Modules, "settings") || slices.Contains(l.Generated, gen.SettingsPath) {
		t.Errorf("lock = %+v", l)
	}
	if again := build(t, dir); !again.UpToDate() {
		t.Errorf("plan after removal: %v", again.Pending())
	}

	// Enabling it again reuses the schema the project kept.
	writeFile(t, dir, "settings.yaml", "settings:\n  api:\n    rps: { type: int, default: 7 }\n")
	writeFile(t, dir, spec.FileName, withSettings)
	p = execute(t, dir)
	if len(p.Create) != 0 {
		t.Errorf("an existing schema was recreated: %v", p.Create)
	}
	code, err := os.ReadFile(filepath.Join(dir, gen.SettingsPath))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(code), "Rps() int") {
		t.Errorf("the kept schema was not used:\n%s", code)
	}
}

// A generated file somebody took over by hand is not deleted with its module.
func TestHandEditedFileIsKept(t *testing.T) {
	dir := project(t, withSettings)
	execute(t, dir)

	writeFile(t, dir, gen.SettingsPath, "package settings\n\n// Taken over by hand.\n")
	writeFile(t, dir, spec.FileName, withoutSettings)

	p := execute(t, dir)
	if len(p.Delete) != 0 || !slices.Equal(p.Kept, []string{gen.SettingsPath}) {
		t.Errorf("delete = %v, kept = %v", p.Delete, p.Kept)
	}
	if !exists(dir, gen.SettingsPath) {
		t.Error("a file taken over by hand was deleted")
	}
}

func TestManualEditOfGeneratedFileIsRewritten(t *testing.T) {
	dir := project(t, withSettings)
	execute(t, dir)

	writeFile(t, dir, gen.ModulesPath, "// edited\n")
	p := build(t, dir)
	if !slices.Equal(p.Write, []string{gen.ModulesPath}) || p.UpToDate() {
		t.Errorf("write = %v", p.Write)
	}
}

// The lock is a file in the repository: a path in it must never make apply delete
// something outside the project.
func TestLockCannotDeleteOutsideProject(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "project")
	writeFile(t, dir, spec.FileName, withoutSettings)
	writeFile(t, root, "victim.go", "// Code generated by platformgo from platformgo.yaml. DO NOT EDIT.\n")
	if err := lock.Write(dir, lock.Lock{Modules: []string{"postgres"}, Generated: []string{"../victim.go"}}); err != nil {
		t.Fatal(err)
	}

	p := execute(t, dir)
	if len(p.Delete) != 0 {
		t.Errorf("delete = %v", p.Delete)
	}
	if !exists(root, "victim.go") {
		t.Fatal("a file outside the project was deleted")
	}
}

func TestSchemaPathMustStayInside(t *testing.T) {
	dir := project(t, strings.Replace(withSettings, "settings: {}", "settings:\n    schema: ../outside.yaml", 1))
	if _, err := apply.Build(dir); err == nil || !strings.Contains(err.Error(), "inside the project") {
		t.Fatalf("err = %v", err)
	}
}

func TestBrokenDescription(t *testing.T) {
	dir := project(t, "schema: 1\nmodules:\n  redis: {}\n")
	if _, err := apply.Build(dir); err == nil {
		t.Fatal("a broken platformgo.yaml was accepted")
	}
}

func TestPending(t *testing.T) {
	dir := project(t, withSettings)
	p := build(t, dir)
	pending := p.Pending()
	for _, want := range []string{"settings.yaml", gen.SettingsPath, lock.FileName} {
		if !slices.Contains(pending, want) {
			t.Errorf("pending lacks %s: %v", want, pending)
		}
	}
	if !slices.IsSorted(pending) {
		t.Errorf("pending is not sorted: %v", pending)
	}
}

func TestModuleToolsGoIntoGoMod(t *testing.T) {
	dir := project(t, withoutSettings)
	writeFile(t, dir, "go.mod", "module example.com/shop-api\n\ngo 1.27\n\ntool golang.org/x/tools/cmd/stringer\n")

	p := build(t, dir)
	var added []string
	for _, tool := range p.AddTools {
		added = append(added, tool.Package)
	}
	slices.Sort(added)
	if !slices.Equal(added, []string{"github.com/go-jet/jet/v2/cmd/jet", "github.com/sqlc-dev/sqlc/cmd/sqlc"}) {
		t.Fatalf("add tools = %v", added)
	}
	if !slices.Contains(p.Pending(), "go.mod") {
		t.Errorf("pending lacks go.mod: %v", p.Pending())
	}
	if err := p.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	mod, err := gomod.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"github.com/sqlc-dev/sqlc/cmd/sqlc", "github.com/go-jet/jet/v2/cmd/jet", "golang.org/x/tools/cmd/stringer"} {
		if !mod.HasTool(want) {
			t.Errorf("go.mod lacks the tool %s: %+v", want, mod)
		}
	}
	if mod.Requires["github.com/sqlc-dev/sqlc"] != "v1.31.1" || mod.Requires["github.com/go-jet/jet/v2"] != "v2.16.0" {
		t.Errorf("pinned versions = %v", mod.Requires)
	}
	if again := build(t, dir); !again.UpToDate() {
		t.Errorf("plan after Execute: %v", again.Pending())
	}

	// Without the module its tools go away; a tool the project declared itself stays.
	writeFile(t, dir, spec.FileName, "schema: 1\nproject:\n  module: example.com/shop-api\nmodules: {}\n")
	p = build(t, dir)
	slices.Sort(p.DropTools)
	if !slices.Equal(p.DropTools, []string{"github.com/go-jet/jet/v2/cmd/jet", "github.com/sqlc-dev/sqlc/cmd/sqlc"}) {
		t.Fatalf("drop tools = %v", p.DropTools)
	}
	if err := p.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if mod, err = gomod.Read(dir); err != nil {
		t.Fatal(err)
	}
	if mod.HasTool("github.com/sqlc-dev/sqlc/cmd/sqlc") || mod.HasTool("github.com/go-jet/jet/v2/cmd/jet") {
		t.Errorf("the tools of the removed module stayed: %v", mod.Tools)
	}
	if !mod.HasTool("golang.org/x/tools/cmd/stringer") {
		t.Errorf("the project's own tool was removed: %v", mod.Tools)
	}
	if again := build(t, dir); !again.UpToDate() {
		t.Errorf("plan after removal: %v", again.Pending())
	}
}

// Enabling rbac gives the project a policy to edit and embeds it; removing the module
// keeps the policy, which belongs to the project.
func TestRBACPolicy(t *testing.T) {
	dir := project(t, "schema: 1\nproject:\n  module: example.com/shop-api\nmodules:\n  api: {}\n  rbac: {}\n")
	p := execute(t, dir)
	if !slices.Contains(p.Create, gen.PolicyPath) || !exists(dir, gen.PolicyGoPath) {
		t.Fatalf("create = %v", p.Create)
	}

	writeFile(t, dir, spec.FileName, "schema: 1\nproject:\n  module: example.com/shop-api\nmodules:\n  api: {}\n")
	p = execute(t, dir)
	if !slices.Contains(p.Delete, gen.PolicyGoPath) || !exists(dir, gen.PolicyPath) {
		t.Errorf("delete = %v, policy kept = %v", p.Delete, exists(dir, gen.PolicyPath))
	}
}
