package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// A reference to a name a codemod could not rewrite keeps verify red until it is fixed:
// plan and apply print it, verify and generate --check fail.
func TestManualStepsKeepVerifyRed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	mustRun(t, "new", "example.com/shop-api", "--with", "postgres", "--dir", dir)
	totp := filepath.Join(dir, "internal", "users", "totp.go")
	if err := os.MkdirAll(filepath.Dir(totp), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package users\n\nimport \"github.com/aidarbn/platform-go/kit/adminx\"\n\nvar _ = adminx.ErrCodeRequired\n"
	if err := os.WriteFile(totp, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	const note = "manual internal/users/totp.go:5: one time codes are gone in v0.6.0: drop the handling of adminx.ErrCodeRequired (adminx.no-totp)"
	for _, cmd := range []string{"plan", "apply"} {
		args := []string{cmd, "--dir", dir}
		if cmd == "apply" {
			args = append(args, "--no-tidy")
		}
		if out := mustRun(t, args...); !strings.Contains(out, note) {
			t.Errorf("%s does not print the manual step:\n%s", cmd, out)
		}
	}
	for _, args := range [][]string{{"verify", "--dir", dir}, {"generate", "--check", "--dir", dir}} {
		_, err := output(t, args...)
		if err == nil || !strings.Contains(err.Error(), "manual steps of a platform upgrade are open:\n"+note) {
			t.Errorf("%s: %v", args[0], err)
		}
	}

	if err := os.Remove(totp); err != nil {
		t.Fatal(err)
	}
	if out := mustRun(t, "verify", "--dir", dir); !strings.Contains(out, "up to date") {
		t.Errorf("verify after the fix:\n%s", out)
	}
}

func TestUpgradeCommand(t *testing.T) {
	if _, err := output(t, "upgrade", "--dir", t.TempDir(), "--dry-run"); err == nil || !strings.Contains(err.Error(), "platformgo.yaml") {
		t.Errorf("an upgrade outside a project: %v", err)
	}
	if _, err := output(t, "upgrade", "v1.0.0", "--bogus"); err == nil {
		t.Error("an unknown flag was accepted")
	}
}

func TestGoRunner(t *testing.T) {
	var out strings.Builder
	if err := goRunner(context.Background(), t.TempDir(), &out, "env", "GOOS"); err != nil || strings.TrimSpace(out.String()) == "" {
		t.Errorf("go env: %q, %v", out.String(), err)
	}
	if err := goRunner(context.Background(), t.TempDir(), io.Discard, "no-such-command"); err == nil {
		t.Error("a failing go command did not fail")
	}
}

func TestDelegateUpgrade(t *testing.T) {
	for _, tc := range []struct {
		command, pinned, self string
		want                  bool
	}{
		{"apply", "v0.5.11", "v0.9.0", true},
		{"upgrade", "v0.5.11", "v0.9.0", false},     // the newer binary drives the upgrade
		{"upgrade", "v0.9.0", "v0.5.11", true},      // the project knows more
		{"upgrade", "local", "v0.9.0", false},       // a working copy
		{"upgrade", "v0.9.0", "dev+abcdef0", false}, // a build of a working copy
	} {
		if got := delegateUpgrade(tc.command, tc.pinned, tc.self); got != tc.want {
			t.Errorf("delegateUpgrade(%q, %q, %q) = %v", tc.command, tc.pinned, tc.self, got)
		}
	}
}

func TestUpgradeCommandReports(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	mustRun(t, "new", "example.com/shop-api", "--with", "postgres", "--dir", dir)
	// go get of the fake moves the platform to the version after @, the rest succeeds.
	old := upgradeRunner
	t.Cleanup(func() { upgradeRunner = old })
	upgradeRunner = func(_ context.Context, dir string, out io.Writer, args ...string) error {
		if args[0] == "get" {
			version := strings.SplitN(args[1], "@", 2)[1]
			cmd := exec.Command("go", "mod", "edit", "-require=github.com/aidarbn/platform-go@"+version)
			cmd.Dir = dir
			return cmd.Run()
		}
		return nil
	}
	mod, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
	current := regexp.MustCompile(`github.com/aidarbn/platform-go (v\S+)`).FindSubmatch(mod)[1]

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"upgrade", string(current), "--dir", dir}, "already on " + string(current)},
		{[]string{"upgrade", "--dry-run", "v0.99.0", "--dir", dir}, "platform    " + string(current) + " → v0.99.0"},
		{[]string{"upgrade", "--no-check", "v0.99.0", "--dir", dir}, "upgraded " + string(current) + " → v0.99.0"},
	} {
		out := mustRun(t, tc.args...)
		if !strings.Contains(out, tc.want) {
			t.Errorf("%v:\n%s", tc.args, out)
		}
		if strings.Contains(out, "upgraded") && slices.Contains(tc.args, "--dry-run") {
			t.Errorf("a dry run says it upgraded:\n%s", out)
		}
	}
}
