package upgrade_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/upgrade"
)

func project(t *testing.T, description string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"platformgo.yaml": description,
		"go.mod":          "module example.com/shop\n\ngo 1.27\n\nrequire github.com/aidarbn/platform-go v0.2.1\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// fakeGo behaves like go get: it moves the platform require to the asked version.
func fakeGo(t *testing.T, calls *[][]string) upgrade.Runner {
	t.Helper()
	return func(_ context.Context, dir string, _ io.Writer, args ...string) error {
		*calls = append(*calls, args)
		if args[0] == "get" {
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
}

const description = `# yaml-language-server: $schema=https://raw.githubusercontent.com/aidarbn/platform-go/v0.2.1/schema/platformgo.schema.json
schema: 1
platform: v0.2.1

project:
  module: example.com/shop   # the service of orders

modules:
  postgres: {}
`

func TestUpgrade(t *testing.T) {
	dir := project(t, description)
	var calls [][]string

	res, err := upgrade.Run(context.Background(), dir, "v0.2.5", fakeGo(t, &calls), io.Discard)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.From != "v0.2.1" || res.To != "v0.2.5" {
		t.Errorf("result = %+v", res)
	}

	if len(calls) != 2 || !slices.Equal(calls[0], []string{"get", "github.com/aidarbn/platform-go@v0.2.5"}) ||
		!slices.Equal(calls[1], []string{"tool", "platformgo", "apply"}) {
		t.Errorf("calls = %v", calls)
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

func TestUpgradeToLatestAddsMissingPlatformLine(t *testing.T) {
	dir := project(t, "schema: 1\nproject:\n  module: example.com/shop\nmodules:\n  postgres: {}\n")
	var calls [][]string

	res, err := upgrade.Run(context.Background(), dir, "", fakeGo(t, &calls), io.Discard)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.To != "v0.3.0" || calls[0][1] != "github.com/aidarbn/platform-go@latest" {
		t.Errorf("res = %+v, calls = %v", res, calls)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "platformgo.yaml"))
	if !strings.Contains(string(raw), "platform: v0.3.0\nschema: 1") {
		t.Errorf("platformgo.yaml:\n%s", raw)
	}
}

func TestUpgradeOutsideProject(t *testing.T) {
	var calls [][]string
	if _, err := upgrade.Run(context.Background(), t.TempDir(), "v1.0.0", fakeGo(t, &calls), io.Discard); err == nil {
		t.Fatal("an upgrade outside a project was accepted")
	}
	if len(calls) != 0 {
		t.Errorf("go ran: %v", calls)
	}
}
