package lock_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/lock"
)

func TestReadMissingLock(t *testing.T) {
	l, err := lock.Read(t.TempDir())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if l.Platform != "" || len(l.Modules) != 0 || len(l.Generated) != 0 {
		t.Errorf("lock = %+v", l)
	}
}

func TestWriteAndRead(t *testing.T) {
	dir := t.TempDir()
	want := lock.Lock{
		Platform:  "v0.1.0",
		Modules:   []string{"settings", "postgres", "postgres"},
		Generated: []string{"cmd/app/modules.gen.go", ".env.example"},
	}
	if err := lock.Write(dir, want); err != nil {
		t.Fatalf("Write: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, lock.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "# Written by platformgo apply") {
		t.Errorf("no header:\n%s", raw)
	}

	got, err := lock.Read(dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// Sorted and without duplicates, so the file only changes when the content does.
	if strings.Join(got.Modules, ",") != "postgres,settings" {
		t.Errorf("modules = %v", got.Modules)
	}
	if strings.Join(got.Generated, ",") != ".env.example,cmd/app/modules.gen.go" {
		t.Errorf("generated = %v", got.Generated)
	}
	if got.Platform != "v0.1.0" {
		t.Errorf("platform = %q", got.Platform)
	}

	// Writing the same lock twice gives the same bytes.
	again, err := lock.Marshal(lock.Lock{Platform: "v0.1.0", Modules: []string{"postgres", "settings"}, Generated: got.Generated})
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(raw) {
		t.Errorf("not deterministic:\n%s\n---\n%s", raw, again)
	}
}

func TestReadBrokenLock(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, lock.FileName), []byte("modules: {"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := lock.Read(dir); err == nil || !strings.Contains(err.Error(), lock.FileName) {
		t.Fatalf("err = %v", err)
	}
}

func TestCompare(t *testing.T) {
	applied := lock.Lock{
		Modules:   []string{"postgres", "settings"},
		Generated: []string{"cmd/app/modules.gen.go", "internal/settings/settings.gen.go"},
	}
	wanted := lock.Lock{
		Modules:   []string{"admin", "postgres"},
		Generated: []string{"cmd/app/modules.gen.go"},
	}

	d := lock.Compare(applied, wanted)
	if strings.Join(d.AddedModules, ",") != "admin" || strings.Join(d.RemovedModules, ",") != "settings" {
		t.Errorf("modules: %+v", d)
	}
	if strings.Join(d.StaleFiles, ",") != "internal/settings/settings.gen.go" {
		t.Errorf("stale = %v", d.StaleFiles)
	}
	if d.Empty() {
		t.Error("the diff is not empty")
	}
	if !lock.Compare(wanted, wanted).Empty() {
		t.Error("a lock compared with itself must give an empty diff")
	}
}

func TestSafePath(t *testing.T) {
	for path, want := range map[string]bool{
		"cmd/app/modules.gen.go": true,
		".env.example":           true,
		"":                       false,
		".":                      false,
		"..":                     false,
		"../outside.go":          false,
		"a/../../outside.go":     false,
		"/etc/passwd":            false,
	} {
		if got := lock.SafePath(path); got != want {
			t.Errorf("SafePath(%q) = %v, want %v", path, got, want)
		}
	}
}
