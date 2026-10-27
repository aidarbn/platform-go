package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/apply"
	"github.com/aidarbn/platform-go/internal/gen"
	"github.com/aidarbn/platform-go/internal/lock"
	"github.com/aidarbn/platform-go/internal/spec"
)

// A service that was not made by the platform: platformgo.yaml next to files of its own.
func foreignService(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for path, content := range map[string]string{
		spec.FileName:     "schema: 1\nproject:\n  module: example.com/shop\nmodules:\n  postgres: {}\n",
		gen.ComposePath:   "services:\n  db:\n    image: postgres:16\n",
		"cmd/app/main.go": "package main\n\nfunc main() {}\n",
	} {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestApplyRefusesConflicts(t *testing.T) {
	dir := foreignService(t)

	out := mustRun(t, "plan", "--diff", "--dir", dir)
	for _, want := range []string{"! docker-compose.yml: the project's own", "--- docker-compose.yml (project)", "-    image: postgres:16"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(mustRun(t, "plan", "--dir", dir), "--- docker-compose.yml") {
		t.Error("plan shows the diff without --diff")
	}

	out, err := output(t, "apply", "--no-tidy", "--dir", dir)
	if err == nil || !strings.Contains(err.Error(), "apply --adopt") || !strings.Contains(out, "! docker-compose.yml") {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, lock.FileName)); !os.IsNotExist(err) {
		t.Error("a refused apply wrote the lock")
	}
	if _, err := output(t, "generate", "--dir", dir); err == nil {
		t.Error("generate overwrote or ignored a conflict")
	}

	out = mustRun(t, "apply", "--adopt", "--dir", dir)
	for _, want := range []string{"created " + gen.ConfigPath, "wrote " + lock.FileName, "! docker-compose.yml", "report " + apply.AdoptReport} {
		if !strings.Contains(out, want) {
			t.Errorf("apply --adopt lacks %q:\n%s", want, out)
		}
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, gen.ComposePath)); string(raw) != "services:\n  db:\n    image: postgres:16\n" {
		t.Errorf("docker-compose.yml was changed:\n%s", raw)
	}
	if _, err := output(t, "verify", "--dir", dir); err == nil || !strings.Contains(err.Error(), "the project's own: docker-compose.yml") {
		t.Errorf("verify: %v", err)
	}

	if _, err := output(t, "apply", "--no-tidy", "--force", "cmd/app/main.go", "--dir", dir); err == nil || !strings.Contains(err.Error(), "not a conflict") {
		t.Errorf("--force of a file that is not a conflict: %v", err)
	}
	out = mustRun(t, "apply", "--no-tidy", "--force", gen.ComposePath, "--dir", dir)
	if !strings.Contains(out, "wrote "+gen.ComposePath) {
		t.Errorf("apply --force:\n%s", out)
	}
	if out := mustRun(t, "verify", "--dir", dir); !strings.Contains(out, "up to date") {
		t.Errorf("verify after --force:\n%s", out)
	}
}

func TestStringListFlag(t *testing.T) {
	var l stringList
	for _, v := range []string{"a, b", "", "c"} {
		if err := l.Set(v); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(l, "|") != "a|b|c" {
		t.Errorf("list = %v", l)
	}
}

func TestDoctorReportsConflicts(t *testing.T) {
	dir := foreignService(t)
	out, err := output(t, "doctor", "--dir", dir)
	if err == nil || !strings.Contains(out, "the project's own versions of generated files: docker-compose.yml") {
		t.Errorf("doctor: %v\n%s", err, out)
	}
}
