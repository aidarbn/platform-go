package main

import (
	"os"
	"path/filepath"
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
