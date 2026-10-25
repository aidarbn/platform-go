package upgrade_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/upgrade"
)

const goMod = "module example.com/shop\n\ngo 1.27\n\nrequire github.com/aidarbn/platform-go v0.2.1\n"

func project(t *testing.T, description string, extra ...string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"platformgo.yaml": description,
		"go.mod":          goMod,
	}
	for i := 0; i+1 < len(extra); i += 2 {
		files[extra[i]] = extra[i+1]
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// fakeGo behaves like the go command: go get moves the platform require to the asked
// version, the rest answers as configured.
type fakeGo struct {
	calls  [][]string
	dirs   []string
	fail   map[string]error  // by the arguments joined with spaces
	output map[string]string // by the arguments joined with spaces
	onGet  func(dir string)
}

func (g *fakeGo) run(_ context.Context, dir string, out io.Writer, args ...string) error {
	g.calls = append(g.calls, args)
	g.dirs = append(g.dirs, dir)
	key := strings.Join(args, " ")
	if text, ok := g.output[key]; ok {
		fmt.Fprint(out, text)
	}
	if err := g.fail[key]; err != nil {
		return err
	}
	if args[0] == "get" {
		if g.onGet != nil {
			g.onGet(dir)
		}
		version := strings.SplitN(args[1], "@", 2)[1]
		if version == "latest" {
			version = "v0.3.0"
		}
		path := filepath.Join(dir, "go.mod")
		raw, _ := os.ReadFile(path)
		text := strings.Replace(string(raw), "platform-go v0.2.1", "platform-go "+version, 1)
		return os.WriteFile(path, []byte(text), 0o600)
	}
	return nil
}

func (g *fakeGo) commands() []string {
	var out []string
	for _, c := range g.calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

const description = `# yaml-language-server: $schema=https://raw.githubusercontent.com/aidarbn/platform-go/v0.2.1/schema/platformgo.schema.json
schema: 1
platform: v0.2.1

project:
  module: example.com/shop   # the service of orders

modules:
  postgres: {}
`

var (
	upgradeCalls = []string{
		"mod tidy -diff",
		"get github.com/aidarbn/platform-go@v0.2.5",
		"tool platformgo apply",
	}
	checkCalls = []string{"build ./...", "tool platformgo verify", "test ./..."}
)

func TestUpgrade(t *testing.T) {
	dir := project(t, description)
	g := &fakeGo{}
	var out bytes.Buffer

	res, err := upgrade.Run(context.Background(), dir, upgrade.Options{Version: "v0.2.5"}, g.run, &out)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.From != "v0.2.1" || res.To != "v0.2.5" || res.Untidy != "" || len(res.Failed()) != 0 {
		t.Errorf("result = %+v", res)
	}
	if want := slices.Concat(upgradeCalls, checkCalls); !slices.Equal(g.commands(), want) {
		t.Errorf("calls = %q, want %q", g.commands(), want)
	}
	if len(res.Checks) != 3 {
		t.Errorf("checks = %+v", res.Checks)
	}
	if strings.Contains(out.String(), "warning") {
		t.Errorf("a tidy project was warned about:\n%s", out.String())
	}

	raw, _ := os.ReadFile(filepath.Join(dir, "platformgo.yaml"))
	got := string(raw)
	for _, want := range []string{"platform: v0.2.5\n", "platform-go/v0.2.5/schema/platformgo.schema.json", "# the service of orders"} {
		if !strings.Contains(got, want) {
			t.Errorf("platformgo.yaml lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "v0.2.1") {
		t.Errorf("the old version stayed:\n%s", got)
	}
}

func TestUpgradeNoCheck(t *testing.T) {
	g := &fakeGo{}
	res, err := upgrade.Run(context.Background(), project(t, description), upgrade.Options{Version: "v0.2.5", NoCheck: true}, g.run, io.Discard)
	if err != nil || len(res.Checks) != 0 || !slices.Equal(g.commands(), upgradeCalls) {
		t.Errorf("res = %+v, err = %v, calls = %q", res, err, g.commands())
	}
}

func TestUpgradeToTheSameVersionChecksNothing(t *testing.T) {
	g := &fakeGo{}
	res, err := upgrade.Run(context.Background(), project(t, description), upgrade.Options{Version: "v0.2.1"}, g.run, io.Discard)
	if err != nil || res.From != res.To || len(res.Checks) != 0 {
		t.Errorf("res = %+v, err = %v, calls = %q", res, err, g.commands())
	}
}

func TestUpgradeFailedChecks(t *testing.T) {
	dir := project(t, description)
	g := &fakeGo{fail: map[string]error{"test ./...": errors.New("exit status 1"), "tool platformgo verify": errors.New("exit status 1")}}

	res, err := upgrade.Run(context.Background(), dir, upgrade.Options{Version: "v0.2.5"}, g.run, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "v0.2.1 → v0.2.5 is applied, but checks failed: platformgo verify, go test") {
		t.Fatalf("err = %v", err)
	}
	if !slices.Equal(res.Failed(), []string{"platformgo verify", "go test"}) {
		t.Errorf("failed = %v", res.Failed())
	}
	// The upgrade stays for the developer to fix or revert.
	if raw, _ := os.ReadFile(filepath.Join(dir, "go.mod")); !strings.Contains(string(raw), "v0.2.5") {
		t.Errorf("go.mod:\n%s", raw)
	}
}

func TestUpgradeWarnsAboutUntidyGoMod(t *testing.T) {
	diff := "--- go.mod\n+++ go.mod\n-require example.com/unused v1.0.0\n"
	g := &fakeGo{output: map[string]string{"mod tidy -diff": diff}, fail: map[string]error{"mod tidy -diff": errors.New("exit status 1")}}
	var out bytes.Buffer

	res, err := upgrade.Run(context.Background(), project(t, description), upgrade.Options{Version: "v0.2.5", NoCheck: true}, g.run, &out)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Untidy != diff {
		t.Errorf("untidy = %q", res.Untidy)
	}
	if !strings.Contains(out.String(), "warning: go.mod was not tidy before the upgrade") || !strings.Contains(out.String(), "    -require example.com/unused v1.0.0") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestUpgradeTidyCheckThatCannotRun(t *testing.T) {
	g := &fakeGo{fail: map[string]error{"mod tidy -diff": errors.New("exit status 1")}, output: map[string]string{"mod tidy -diff": "go: downloading x: no network\n"}}
	var out bytes.Buffer
	res, err := upgrade.Run(context.Background(), project(t, description), upgrade.Options{Version: "v0.2.5", NoCheck: true}, g.run, &out)
	if err != nil || res.Untidy != "" || res.TidyErr != "exit status 1\ngo: downloading x: no network" {
		t.Errorf("res = %+v, err = %v", res, err)
	}
	if !strings.Contains(out.String(), "warning: could not check that go.mod was tidy before the upgrade:\n    exit status 1\n    go: downloading x: no network\n") ||
		strings.Contains(out.String(), "was not tidy") {
		t.Errorf("output:\n%s", out.String())
	}

	out.Reset()
	if _, err := upgrade.Run(context.Background(), project(t, description), upgrade.Options{Version: "v0.2.5", DryRun: true, NoCheck: true}, g.run, &out); err != nil ||
		!strings.Contains(out.String(), "go.mod      could not check that it is tidy:\n    exit status 1") {
		t.Errorf("dry run: %v\n%s", err, out.String())
	}
}

func TestUpgradeToLatestAddsMissingPlatformLine(t *testing.T) {
	dir := project(t, "schema: 1\nproject:\n  module: example.com/shop\nmodules:\n  postgres: {}\n")
	g := &fakeGo{}

	res, err := upgrade.Run(context.Background(), dir, upgrade.Options{NoCheck: true}, g.run, io.Discard)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.To != "v0.3.0" || g.calls[1][1] != "github.com/aidarbn/platform-go@latest" {
		t.Errorf("res = %+v, calls = %v", res, g.calls)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "platformgo.yaml"))
	if !strings.Contains(string(raw), "platform: v0.3.0\nschema: 1") {
		t.Errorf("platformgo.yaml:\n%s", raw)
	}
}

func TestUpgradeFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		fail map[string]error
		want string
	}{
		"get":   {map[string]error{"get github.com/aidarbn/platform-go@v0.2.5": errors.New("no such version")}, "upgrade: no such version"},
		"apply": {map[string]error{"tool platformgo apply": errors.New("exit status 1")}, "upgrade: apply of v0.2.5: exit status 1"},
	} {
		t.Run(name, func(t *testing.T) {
			g := &fakeGo{fail: tc.fail}
			_, err := upgrade.Run(context.Background(), project(t, description), upgrade.Options{Version: "v0.2.5"}, g.run, io.Discard)
			if err == nil || err.Error() != tc.want {
				t.Errorf("err = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestUpgradeWithoutThePlatformInGoMod(t *testing.T) {
	dir := project(t, description, "go.mod", "module example.com/shop\n\ngo 1.27\n")
	g := &fakeGo{}
	if _, err := upgrade.Run(context.Background(), dir, upgrade.Options{Version: "v0.2.5"}, g.run, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "go.mod does not require github.com/aidarbn/platform-go") {
		t.Errorf("err = %v", err)
	}
}

func TestUpgradeOutsideProject(t *testing.T) {
	g := &fakeGo{}
	if _, err := upgrade.Run(context.Background(), t.TempDir(), upgrade.Options{Version: "v1.0.0"}, g.run, io.Discard); err == nil {
		t.Fatal("an upgrade outside a project was accepted")
	}
	if len(g.calls) != 0 {
		t.Errorf("go ran: %v", g.calls)
	}
}

func TestDryRunLeavesTheProjectAlone(t *testing.T) {
	dir := project(t, description,
		"go.mod", goMod+"\nreplace example.com/lib => ../lib\n\nreplace example.com/vendored => ./third_party/vendored\n",
		".git/HEAD", "ref: refs/heads/main\n",
		"sub/.git", "gitdir: /somewhere/.git/worktrees/sub\n",
		"node_modules/x/index.js", "x\n",
		"internal/app/app.go", "package app\n",
	)
	if err := os.Symlink("internal/app", filepath.Join(dir, "app")); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, dir)

	plan := "service  shop\nmodules  postgres\n~ internal/app/app.go: codemod adminx.no-totp\nmanual internal/app/app.go:3: adminx.TOTPCode is gone (adminx.no-totp)\n"
	g := &fakeGo{output: map[string]string{"tool platformgo plan": plan}}
	var copyMod string
	g.onGet = func(copyDir string) {
		raw, _ := os.ReadFile(filepath.Join(copyDir, "go.mod"))
		copyMod = string(raw)
		for _, gone := range []string{".git", "node_modules", "sub/.git"} {
			if _, err := os.Stat(filepath.Join(copyDir, gone)); err == nil {
				t.Errorf("%s was copied", gone)
			}
		}
		if target, err := os.Readlink(filepath.Join(copyDir, "app")); err != nil || target != "internal/app" {
			t.Errorf("symlink: %q, %v", target, err)
		}
	}
	var out bytes.Buffer

	res, err := upgrade.Run(context.Background(), dir, upgrade.Options{Version: "v0.2.5", DryRun: true}, g.run, &out)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if res.From != "v0.2.1" || res.To != "v0.2.5" || res.Plan != plan || len(res.Checks) != 3 {
		t.Errorf("result = %+v", res)
	}
	if want := slices.Concat(upgradeCalls[:2], []string{"tool platformgo plan", "tool platformgo apply"}, checkCalls); !slices.Equal(g.commands(), want) {
		t.Errorf("calls = %q, want %q", g.commands(), want)
	}
	for _, d := range g.dirs {
		if d == dir {
			t.Errorf("go ran in the project itself")
		}
	}
	if want := "=> " + filepath.Join(dir, "..", "lib"); !strings.Contains(copyMod, want) {
		t.Errorf("the relative replace was not made absolute, want %q:\n%s", want, copyMod)
	}
	if !strings.Contains(copyMod, "example.com/vendored => ./third_party/vendored") {
		t.Errorf("a replace inside the project was pointed at the original:\n%s", copyMod)
	}
	if after := snapshot(t, dir); !maps.Equal(before, after) {
		t.Errorf("the project changed")
	}
	if _, err := os.Stat(g.dirs[0]); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the copy stayed: %v", err)
	}
	for _, want := range []string{
		"platform    v0.2.1 → v0.2.5\n",
		"config      platformgo.yaml: platform v0.2.5\n",
		"plan of v0.2.5:\n    service  shop\n",
		"    manual internal/app/app.go:3: adminx.TOTPCode is gone",
		"checks:\n  ok    go build\n  ok    platformgo verify\n  ok    go test\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestDryRunReportsFailedChecks(t *testing.T) {
	g := &fakeGo{
		output: map[string]string{"tool platformgo verify": "manual steps of a platform upgrade are open\n", "mod tidy -diff": "diff current/go.mod tidy/go.mod\n-require x v1\n"},
		fail:   map[string]error{"tool platformgo verify": errors.New("exit status 1"), "mod tidy -diff": errors.New("exit status 1")},
	}
	var out bytes.Buffer
	res, err := upgrade.Run(context.Background(), project(t, description), upgrade.Options{Version: "v0.2.5", DryRun: true}, g.run, &out)
	if err == nil || !strings.Contains(err.Error(), "v0.2.1 → v0.2.5 would fail checks: platformgo verify") {
		t.Fatalf("err = %v", err)
	}
	if res.Checks[1].Output == "" {
		t.Errorf("the output of the check is lost: %+v", res.Checks)
	}
	for _, want := range []string{
		"go.mod      not tidy before the upgrade",
		"    -require x v1\n",
		"  fail  platformgo verify: exit status 1\n        manual steps of a platform upgrade are open\n",
		"  ok    go test\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// The copy goes into a folder of its own whatever the project folder is called, ".."
// included, and nothing is left behind.
func TestDryRunOfAParentFolder(t *testing.T) {
	dir := project(t, description)
	inner := filepath.Join(dir, "inner")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	g := &fakeGo{}
	if _, err := upgrade.Run(context.Background(), filepath.Join(inner, ".."), upgrade.Options{Version: "v0.2.5", DryRun: true, NoCheck: true}, g.run, io.Discard); err != nil {
		t.Fatal(err)
	}
	if filepath.Base(g.dirs[0]) != "project" || filepath.Dir(filepath.Dir(g.dirs[0])) != tmp {
		t.Errorf("copied to %s", g.dirs[0])
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Errorf("left in the temporary folder: %v", left)
	}
}

func TestDryRunNoCheck(t *testing.T) {
	g := &fakeGo{}
	var out bytes.Buffer
	res, err := upgrade.Run(context.Background(), project(t, description), upgrade.Options{Version: "v0.2.5", DryRun: true, NoCheck: true}, g.run, &out)
	if err != nil || len(res.Checks) != 0 || strings.Contains(out.String(), "checks:") {
		t.Errorf("res = %+v, err = %v\n%s", res, err, out.String())
	}
}

func TestDryRunOnTheSameVersion(t *testing.T) {
	g := &fakeGo{}
	var out bytes.Buffer
	_, err := upgrade.Run(context.Background(), project(t, description), upgrade.Options{Version: "v0.2.1", DryRun: true}, g.run, &out)
	if err != nil || !strings.Contains(out.String(), "already on v0.2.1") || len(g.calls) != 2 {
		t.Errorf("err = %v, calls = %q\n%s", err, g.commands(), out.String())
	}
}

func TestDryRunFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		key, want string
	}{
		"get":   {"get github.com/aidarbn/platform-go@v0.2.5", "upgrade: boom\n    go says why\n"},
		"plan":  {"tool platformgo plan", "upgrade: plan of v0.2.5: boom\n    go says why\n"},
		"apply": {"tool platformgo apply", "upgrade: apply of v0.2.5: boom\n    go says why\n"},
	} {
		t.Run(name, func(t *testing.T) {
			g := &fakeGo{fail: map[string]error{tc.key: errors.New("boom")}, output: map[string]string{tc.key: "go says why\n"}}
			_, err := upgrade.Run(context.Background(), project(t, description), upgrade.Options{Version: "v0.2.5", DryRun: true}, g.run, io.Discard)
			if err == nil || err.Error() != tc.want {
				t.Errorf("err = %q, want %q", err, tc.want)
			}
		})
	}
}

func TestDryRunOfABrokenGoMod(t *testing.T) {
	dir := project(t, description, "go.mod", "module example.com/shop\n\nrequire (\n")
	g := &fakeGo{}
	if _, err := upgrade.Run(context.Background(), dir, upgrade.Options{DryRun: true}, g.run, io.Discard); err == nil {
		t.Fatal("a broken go.mod was accepted")
	}
}

func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return err
		}
		raw, err := os.ReadFile(path)
		out[path] = string(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
