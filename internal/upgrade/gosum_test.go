package upgrade_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/module"
	"golang.org/x/mod/zip"

	"github.com/aidarbn/platform-go/internal/upgrade"
)

// The platform tool of the fake releases: apply tidies the project as the real apply
// does, the rest succeeds. v0.2.0 imports a module v0.1.0 did not know, as v0.9.0 did
// with pyroscope-go.
const fakeTool = `package main

import (
	"fmt"
	"os"
	"os/exec"
%s)

func main() {
	%s
	if len(os.Args) > 1 && os.Args[1] == "apply" {
		cmd := exec.Command("go", "mod", "tidy")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			os.Exit(1)
		}
	}
	fmt.Println("platformgo", os.Args[1:])
}
`

// The kit package the project imports; in v0.2.0 it gains the new module too.
const fakeKit = `package obs

%s
func Name() string { return "obs" }
`

// release writes a module version into a file proxy, the layout GOPROXY=file:// reads.
func release(t *testing.T, proxy, path, version string, files map[string]string) {
	t.Helper()
	src := t.TempDir()
	for name, content := range files {
		p := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(proxy, path, "@v")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := zip.CreateFromDir(&archive, module.Version{Path: path, Version: version}, src); err != nil {
		t.Fatal(err)
	}
	list, _ := os.ReadFile(filepath.Join(dir, "list"))
	for name, content := range map[string][]byte{
		version + ".zip":  archive.Bytes(),
		version + ".mod":  []byte(files["go.mod"]),
		version + ".info": fmt.Appendf(nil, `{"Version":%q,"Time":"2026-01-01T00:00:00Z"}`, version),
		"list":            append(list, version+"\n"...),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// offlineGo runs the real go command against the file proxy only: no network, no
// checksum database, a module cache of the test.
func offlineGo(proxy, cache string) upgrade.Runner {
	return func(ctx context.Context, dir string, out io.Writer, args ...string) error {
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = dir
		cmd.Stdout, cmd.Stderr = out, out
		cmd.Env = append(os.Environ(),
			"GOPROXY=file://"+filepath.ToSlash(proxy),
			"GOSUMDB=off", "GOPRIVATE=", "GONOPROXY=", "GONOSUMDB=", "GOINSECURE=",
			"GOMODCACHE="+cache, "GOFLAGS=-modcacherw", "GOWORK=off", "GOTOOLCHAIN=local",
		)
		return cmd.Run()
	}
}

// A new platform version whose tool and kit need modules the old one did not: upgrade
// must leave go.sum able to build the new tool, or go tool platformgo apply stops at
// "missing go.sum entry" before it can tidy anything. go get of the module root only
// records the checksums of the root, which is how v0.9.0 broke upgrades.
func TestUpgradeToAVersionWithNewDependencies(t *testing.T) {
	if testing.Short() {
		t.Skip("builds modules with the go command")
	}
	goVersion := strings.TrimPrefix(strings.Fields(runtimeGoVersion(t))[0], "go")

	proxy := t.TempDir()
	release(t, proxy, "example.com/dep", "v1.0.0", map[string]string{
		"go.mod": "module example.com/dep\n\ngo 1.22\n",
		"dep.go": "package dep\n\nconst Name = \"dep\"\n",
	})
	const platform = "github.com/aidarbn/platform-go"
	release(t, proxy, platform, "v0.1.0", map[string]string{
		"go.mod":                 "module " + platform + "\n\ngo 1.22\n",
		"cmd/platformgo/main.go": fmt.Sprintf(fakeTool, "", ""),
		"kit/obs/obs.go":         fmt.Sprintf(fakeKit, ""),
	})
	release(t, proxy, platform, "v0.2.0", map[string]string{
		"go.mod":                 "module " + platform + "\n\ngo 1.22\n\nrequire example.com/dep v1.0.0\n",
		"cmd/platformgo/main.go": fmt.Sprintf(fakeTool, "\t\"example.com/dep\"\n", "_ = dep.Name"),
		"kit/obs/obs.go":         fmt.Sprintf(fakeKit, "import _ \"example.com/dep\"\n"),
	})

	run := offlineGo(proxy, t.TempDir())
	newProject := func(t *testing.T) string {
		dir := project(t, strings.ReplaceAll(description, "v0.2.1", "v0.1.0"),
			"go.mod", "module example.com/shop\n\ngo "+goVersion+"\n\nrequire "+platform+" v0.1.0\n\ntool "+platform+"/cmd/platformgo\n",
			"main.go", "package main\n\nimport \"github.com/aidarbn/platform-go/kit/obs\"\n\nfunc main() { println(obs.Name()) }\n",
		)
		var log bytes.Buffer
		if err := run(context.Background(), dir, &log, "mod", "tidy"); err != nil {
			t.Fatalf("go mod tidy: %v\n%s", err, log.String())
		}
		return dir
	}

	t.Run("upgrade", func(t *testing.T) {
		dir := newProject(t)
		var out bytes.Buffer
		res, err := upgrade.Run(context.Background(), dir, upgrade.Options{Version: "v0.2.0", Strict: true}, run, &out)
		if err != nil {
			t.Fatalf("Run: %v\n%s", err, out.String())
		}
		if res.From != "v0.1.0" || res.To != "v0.2.0" || len(res.Failed()) != 0 {
			t.Errorf("result = %+v\n%s", res, out.String())
		}
		sum, _ := os.ReadFile(filepath.Join(dir, "go.sum"))
		if !strings.Contains(string(sum), "example.com/dep v1.0.0 h1:") {
			t.Errorf("go.sum lacks the new dependency:\n%s", sum)
		}
	})

	t.Run("dry run", func(t *testing.T) {
		dir := newProject(t)
		var out bytes.Buffer
		res, err := upgrade.Run(context.Background(), dir, upgrade.Options{Version: "latest", DryRun: true, Strict: true}, run, &out)
		if err != nil {
			t.Fatalf("Run: %v\n%s", err, out.String())
		}
		if res.To != "v0.2.0" || !strings.Contains(out.String(), "checks:\n  ok    go build\n  ok    platformgo verify\n  ok    go test\n") {
			t.Errorf("result = %+v\n%s", res, out.String())
		}
	})
}

func runtimeGoVersion(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOVERSION").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
