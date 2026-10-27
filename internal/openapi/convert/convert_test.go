package convert_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/openapi/convert"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// project copies testdata/in into a temporary project.
func project(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	err := filepath.WalkDir("testdata/in", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel("testdata/in", p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(rel)), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", "out", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v; run go test ./internal/openapi/convert -update", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs; run go test ./internal/openapi/convert -update and review the diff\n%s", p, got)
	}
}

func report(r *convert.Report) []byte {
	var b bytes.Buffer
	for _, p := range r.Changed {
		b.WriteString("changed " + p + "\n")
	}
	b.WriteString("base " + r.Base + "\n")
	for _, k := range sortedKeys(r.Counts) {
		b.WriteString(k + ": " + itoa(r.Counts[k]) + "\n")
	}
	for _, n := range r.Notes {
		b.WriteString(n.String() + "\n")
	}
	return b.Bytes()
}

func TestConvert(t *testing.T) {
	dir := project(t)
	r, err := convert.Run(convert.Options{Dir: dir, ProtoDir: "proto", Base: "api/openapi/base.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "report.txt", report(r))
	for _, f := range []string{"proto/shop/v1/shop.proto", "proto/apidoc/v1/apidoc.proto", "proto/edge/v1/edge.proto", "api/openapi/base.yaml"} {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		golden(t, f, b)
	}
	// A second run finds only what was left for a person.
	again, err := convert.Run(convert.Options{Dir: dir, ProtoDir: "proto", Base: "api/openapi/base.yaml", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "report-again.txt", report(again))
}

func TestDryRunWritesNothing(t *testing.T) {
	dir := project(t)
	before, _ := os.ReadFile(filepath.Join(dir, "proto/shop/v1/shop.proto"))
	r, err := convert.Run(convert.Options{Dir: dir, ProtoDir: "proto", Base: "api/openapi/base.yaml", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "proto/shop/v1/shop.proto"))
	if len(r.Changed) != 3 || !bytes.Equal(before, after) {
		t.Errorf("changed = %v, file rewritten = %v", r.Changed, !bytes.Equal(before, after))
	}
	if _, err := os.Stat(filepath.Join(dir, "api/openapi/base.yaml")); err == nil {
		t.Error("the base was written")
	}
}

// An existing base stays the project's: the converted one goes next to it.
func TestExistingBase(t *testing.T) {
	dir := project(t)
	if err := os.MkdirAll(filepath.Join(dir, "api/openapi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "api/openapi/base.yaml"), []byte("openapi: 3.1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := convert.Run(convert.Options{Dir: dir, ProtoDir: "proto", Base: "api/openapi/base.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Base != "api/openapi/base.openapiv2.yaml" {
		t.Errorf("base = %s", r.Base)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "api/openapi/base.yaml")); string(b) != "openapi: 3.1.0\n" {
		t.Error("the project's base was overwritten")
	}
	if _, err := os.Stat(filepath.Join(dir, "api/openapi/base.openapiv2.yaml")); err != nil {
		t.Error(err)
	}
}

func TestErrors(t *testing.T) {
	if _, err := convert.Run(convert.Options{Dir: t.TempDir(), ProtoDir: "proto"}); err == nil {
		t.Error("no proto folder is not an error")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "proto"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := convert.Run(convert.Options{Dir: dir, ProtoDir: "proto"}); err == nil || !strings.Contains(err.Error(), "no proto files") {
		t.Errorf("err = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "proto", "a.proto"), []byte(`syntax = "proto3"; import "missing.proto";`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := convert.Run(convert.Options{Dir: dir, ProtoDir: "proto"}); err == nil || !strings.Contains(err.Error(), "missing.proto") {
		t.Errorf("err = %v", err)
	}
}

// A file with CRLF line endings keeps them, in the lines the converter adds too.
func TestCRLF(t *testing.T) {
	dir := t.TempDir()
	src := strings.Join([]string{
		`syntax = "proto3";`,
		`package crlf.v1;`,
		``,
		`import "protoc-gen-openapiv2/options/annotations.proto";`,
		``,
		`message M {`,
		`  string name = 1 [(grpc.gateway.protoc_gen_openapiv2.options.openapiv2_field) = {description: "The name.\nTwo lines." example: "\"x\""}];`,
		`}`,
		``,
	}, "\r\n")
	if err := os.MkdirAll(filepath.Join(dir, "proto"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "proto", "m.proto")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := convert.Run(convert.Options{Dir: dir, ProtoDir: "proto", Base: "base.yaml"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if strings.Count(string(b), "\n") != strings.Count(string(b), "\r\n") || !strings.Contains(string(b), "  // The name.\r\n  // Two lines.\r\n") {
		t.Errorf("converted:\n%q", b)
	}
}
