package codemod_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/codemod"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func goldenFile(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs; run go test ./internal/codemod -update and review the diff\n%s", p, got)
	}
}

func TestGnosticOptions(t *testing.T) {
	in, err := os.ReadFile("testdata/gnostic.in.proto")
	if err != nil {
		t.Fatal(err)
	}
	dir := project(t, map[string]string{"proto/shop/v1/shop.proto": string(in)})
	mods := codemod.Applicable([]codemod.Codemod{codemod.GnosticOptions()}, codemod.Project{OpenAPIGenerator: "platform"})
	res, err := codemod.Run(dir, mods, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changes) != 1 {
		t.Fatalf("changes = %d", len(res.Changes))
	}
	goldenFile(t, "gnostic.out.proto", res.Changes[0].Content)
	var notes []string
	for _, n := range res.Notes {
		notes = append(notes, n.String())
	}
	goldenFile(t, "gnostic.notes.txt", []byte(strings.Join(notes, "\n")+"\n"))

	// A project that keeps the connect generator keeps its annotations.
	for _, g := range []string{"connect", "none", ""} {
		if mods := codemod.Applicable([]codemod.Codemod{codemod.GnosticOptions()}, codemod.Project{OpenAPIGenerator: g}); len(mods) != 0 {
			t.Errorf("%q: the codemod runs", g)
		}
	}
}

// A file whose annotations all move loses the gnostic import, which becomes the import
// of the platform's options.
func TestGnosticImportGoes(t *testing.T) {
	src := "syntax = \"proto3\";\r\npackage a.v1;\r\n\r\nimport \"gnostic/openapi/v3/annotations.proto\";\r\n\r\nmessage M {\r\n  string id = 1 [(gnostic.openapi.v3.property) = {description: \"The id.\" example: {yaml: \"3\"}}];\r\n  string n = 2 [(gnostic.openapi.v3.property) = {description: \"N.\"}];\r\n}\r\n"
	dir := project(t, map[string]string{"proto/a.proto": src})
	res, err := codemod.Run(dir, []codemod.Codemod{codemod.GnosticOptions()}, nil)
	if err != nil || len(res.Changes) != 1 || len(res.Notes) != 0 {
		t.Fatalf("res = %+v, %v", res, err)
	}
	want := "syntax = \"proto3\";\r\npackage a.v1;\r\n\r\nimport \"platform/openapi/v1/options.proto\";\r\n\r\nmessage M {\r\n  // The id.\r\n  string id = 1 [(platform.openapi.v1.field) = {example: \"3\"}];\r\n  // N.\r\n  string n = 2;\r\n}\r\n"
	if got := string(res.Changes[0].Content); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

// A file that imports gnostic and has no annotation left loses the import, which buf
// could not resolve without the dependency.
func TestGnosticImportOnly(t *testing.T) {
	src := "syntax = \"proto3\";\npackage a.v1;\n\nimport \"gnostic/openapi/v3/annotations.proto\";\nimport \"google/api/annotations.proto\";\n\nmessage M {}\n"
	dir := project(t, map[string]string{"proto/a.proto": src})
	res, err := codemod.Run(dir, []codemod.Codemod{codemod.GnosticOptions()}, nil)
	if err != nil || len(res.Changes) != 1 {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if got := string(res.Changes[0].Content); strings.Contains(got, "gnostic") || !strings.Contains(got, "google/api/annotations.proto") {
		t.Errorf("got:\n%s", got)
	}
}
