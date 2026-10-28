package openapi_test

import (
	"bytes"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/aidarbn/platform-go/internal/openapi"
	"github.com/aidarbn/platform-go/internal/openapi/oascheck"
)

// The split layout of the shop API: a file per proto file, common.yaml, the root and
// the bundle, all of them golden.
func TestGoldenSplit(t *testing.T) {
	files := compile(t, "testdata/proto", shopFiles...)
	resp := run(t, "base=testdata/base.yaml", shopFiles, files)
	if resp.GetError() != "" {
		t.Fatal(resp.GetError())
	}
	got := map[string][]byte{}
	for _, f := range resp.GetFile() {
		got[f.GetName()] = []byte(f.GetContent())
		golden(t, "split/"+f.GetName(), got[f.GetName()])
	}
	want := []string{"common.yaml", "legacy/v1/legacy.yaml", "openapi.bundle.yaml", "openapi.yaml", "shop/v1/shop.yaml", "shop/v1/types.yaml"}
	var names []string
	for name := range got {
		names = append(names, name)
	}
	slices.Sort(names)
	if !slices.Equal(names, want) {
		t.Errorf("files = %v, want %v", names, want)
	}
	if !*update {
		var onDisk []string
		_ = filepath.WalkDir("testdata/golden/split", func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel("testdata/golden/split", p)
				onDisk = append(onDisk, filepath.ToSlash(rel))
			}
			return nil
		})
		if !slices.Equal(onDisk, want) {
			t.Errorf("golden files = %v: remove the stale ones", onDisk)
		}
	}
	for _, f := range []string{openapi.BundleFile, openapi.RootFile} {
		if err := oascheck.Validate(got[f]); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	// The same proto files in the single layout give the bundle byte for byte.
	single := run(t, "base=testdata/base.yaml,layout=single", shopFiles, files)
	if single.GetFile()[0].GetContent() != string(got[openapi.BundleFile]) {
		t.Error("the bundle differs from the single layout")
	}
	checkSplit(t, got, nil)
}

// checkSplit resolves the root through the files it refers to and compares the result
// with the bundle: every path item, every schema, and every reference points at a
// file and a key that exist. baseSchemas are the schemas the base describes itself:
// they stay in the root.
func checkSplit(t *testing.T, files map[string][]byte, baseSchemas []string) {
	t.Helper()
	docs := map[string]map[string]any{}
	for name, b := range files {
		var v map[string]any
		if err := yaml.Unmarshal(b, &v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		docs[name] = v
	}
	// resolve follows a reference from a file to the value it names.
	resolve := func(from, ref string) (string, any) {
		file, frag, _ := strings.Cut(ref, "#")
		target := from
		if file != "" {
			target = path.Join(path.Dir(from), file)
		}
		doc, ok := docs[target]
		if !ok {
			t.Fatalf("%s: %s refers to a missing file %s", from, ref, target)
		}
		frag, err := url.PathUnescape(frag)
		if err != nil {
			t.Fatal(err)
		}
		var v any = doc
		for _, tok := range strings.Split(strings.TrimPrefix(frag, "/"), "/") {
			tok = strings.NewReplacer("~1", "/", "~0", "~").Replace(tok)
			m, ok := v.(map[string]any)
			if !ok || m[tok] == nil {
				t.Fatalf("%s: %s names nothing in %s", from, ref, target)
			}
			v = m[tok]
		}
		return target, v
	}
	// local rewrites every reference of a value to the form of the bundle, checking
	// that it resolves from the file it is in.
	var local func(from string, v any) any
	local = func(from string, v any) any {
		switch v := v.(type) {
		case map[string]any:
			out := map[string]any{}
			for k, x := range v {
				if ref, ok := x.(string); ok && k == "$ref" {
					resolve(from, ref)
					_, frag, _ := strings.Cut(ref, "#")
					out[k] = "#" + frag
					continue
				}
				out[k] = local(from, x)
			}
			return out
		case []any:
			out := make([]any, len(v))
			for i, x := range v {
				out[i] = local(from, x)
			}
			return out
		}
		return v
	}
	bundle := docs[openapi.BundleFile]
	root := docs[openapi.RootFile]
	bundlePaths := bundle["paths"].(map[string]any)
	rootPaths := root["paths"].(map[string]any)
	if len(rootPaths) != len(bundlePaths) {
		t.Errorf("root paths = %d, bundle paths = %d", len(rootPaths), len(bundlePaths))
	}
	for p, item := range rootPaths {
		from, v := openapi.RootFile, any(item)
		if ref, ok := item.(map[string]any)["$ref"].(string); ok {
			from, v = resolve(openapi.RootFile, ref)
		}
		if !equal(local(from, v), bundlePaths[p]) {
			t.Errorf("path %s differs from the bundle", p)
		}
	}
	schemas := map[string]any{}
	for name, doc := range docs {
		if name == openapi.BundleFile {
			continue
		}
		comps, _ := doc["components"].(map[string]any)
		for _, kind := range []string{"schemas", "responses"} {
			m, _ := comps[kind].(map[string]any)
			for k, v := range m {
				if name == openapi.RootFile && (kind != "schemas" || !slices.Contains(baseSchemas, k)) && (kind != "responses" || !slices.Contains(baseSchemas, "responses/"+k)) {
					t.Errorf("the root holds %s %s", kind, k)
				}
				if _, dup := schemas[kind+"/"+k]; dup {
					t.Errorf("%s %s is in two files", kind, k)
				}
				schemas[kind+"/"+k] = local(name, v)
			}
		}
	}
	comps := bundle["components"].(map[string]any)
	for _, kind := range []string{"schemas", "responses"} {
		for k, v := range comps[kind].(map[string]any) {
			if !equal(schemas[kind+"/"+k], local(openapi.BundleFile, v)) {
				t.Errorf("%s %s differs from the bundle", kind, k)
			}
			delete(schemas, kind+"/"+k)
		}
	}
	if len(schemas) > 0 {
		t.Errorf("components the bundle lacks: %v", schemas)
	}
	// The root keeps the human part of the base and only the components the base has.
	if root["info"] == nil || root["servers"] == nil {
		t.Error("the root lost the base")
	}
}

func equal(a, b any) bool {
	x, _ := yaml.Marshal(a)
	y, _ := yaml.Marshal(b)
	return bytes.Equal(x, y)
}

// A proto file whose yaml would be a file of the description itself fails, whatever
// the case of its name.
func TestSplitNameCollision(t *testing.T) {
	files := compile(t, "testdata/bad", "Common.proto")
	resp := run(t, "", []string{"Common.proto"}, files)
	if want := "Common.proto would be written to common.yaml"; !strings.Contains(resp.GetError(), want) {
		t.Errorf("error = %q, want %q", resp.GetError(), want)
	}
	if resp := run(t, "layout=single", []string{"Common.proto"}, files); resp.GetError() != "" {
		t.Errorf("the single layout has no such files: %s", resp.GetError())
	}
}

// A project with an error handler of its own describes its error body in the base: the
// generator keeps that schema and response, drops the details only its own body names,
// and every operation points at the base's, in both layouts.
func TestBaseError(t *testing.T) {
	files := compile(t, "testdata/proto", shopFiles...)
	resp := run(t, "base=testdata/base-error.yaml", shopFiles, files)
	if resp.GetError() != "" {
		t.Fatal(resp.GetError())
	}
	got := map[string][]byte{}
	for _, f := range resp.GetFile() {
		got[f.GetName()] = []byte(f.GetContent())
	}
	for _, f := range []string{openapi.BundleFile, openapi.RootFile} {
		if err := oascheck.Validate(got[f]); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	checkSplit(t, got, []string{"Error", "ErrorDetail", "responses/Error"})

	bundle := string(got[openapi.BundleFile])
	for _, want := range []string{"request_id:", "description: An error of the shop."} {
		if !strings.Contains(bundle, want) {
			t.Errorf("the bundle lacks %q", want)
		}
	}
	for _, gone := range []string{"google.rpc.Status", "buf.validate.Violations:", "google.rpc.ErrorInfo:", "buf.validate.FieldPath:"} {
		if strings.Contains(bundle, gone) {
			t.Errorf("the bundle still has %q", gone)
		}
	}
	if strings.Contains(string(got[openapi.CommonFile]), "Error:") {
		t.Errorf("common.yaml still describes the error:\n%s", got[openapi.CommonFile])
	}
	if !strings.Contains(string(got["shop/v1/shop.yaml"]), "../../openapi.yaml#/components/responses/Error") {
		t.Errorf("an operation does not point at the error of the base:\n%s", got["shop/v1/shop.yaml"])
	}

	// Only the schema in the base: the generated response points at it in the root.
	base, err := os.ReadFile("testdata/base-error.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cut, _, _ := strings.Cut(string(base), "  responses:\n")
	only := filepath.Join(t.TempDir(), "base.yaml")
	if err := os.WriteFile(only, []byte(cut), 0o644); err != nil {
		t.Fatal(err)
	}
	resp = run(t, "base="+only, shopFiles, files)
	if resp.GetError() != "" {
		t.Fatal(resp.GetError())
	}
	got = map[string][]byte{}
	for _, f := range resp.GetFile() {
		got[f.GetName()] = []byte(f.GetContent())
	}
	checkSplit(t, got, []string{"Error", "ErrorDetail"})
	if !strings.Contains(string(got[openapi.CommonFile]), "openapi.yaml#/components/schemas/Error") {
		t.Errorf("the generated response does not point at the schema of the base:\n%s", got[openapi.CommonFile])
	}
}
