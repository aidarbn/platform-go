package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	docs := filepath.Join(t.TempDir(), "upgrades.md")
	if err := os.WriteFile(docs, []byte("## Manual steps\n\n### v0.9.0\n\n- `kit/x.Gone`\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report := "Incompatible changes:\n- github.com/aidarbn/platform-go/kit/x.Gone: removed\n- github.com/aidarbn/platform-go/kit/x.Left: removed\n"
	var out strings.Builder
	if err := run([]string{"-base", "v0.8.0", "-release", "v0.9.0", "-docs", docs}, strings.NewReader(report), &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "kit/x.Left\n" {
		t.Errorf("out = %q", out.String())
	}
	if err := run([]string{"-docs", filepath.Join(t.TempDir(), "none.md")}, strings.NewReader(""), &out); err == nil {
		t.Error("a missing docs file was accepted")
	}
	if err := run([]string{"-bogus"}, strings.NewReader(""), &out); err == nil {
		t.Error("an unknown flag was accepted")
	}
}
