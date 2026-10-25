package codemod_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/codemod"
)

const shopProto = `syntax = "proto3";

package shop.v1;

import "google/api/annotations.proto";
import public "gnostic/openapi/v3/annotations.proto"; // the docs
/* import "commented/out.proto"; */

option (gnostic.openapi.v3.document) = {
  info: { title: "Shop; the API" version: "1" } // a } in a comment
};
option go_package = "example.com/shop/gen;shopv1";

service Shop {
  rpc Get(GetRequest) returns (GetResponse) {
    option (google.api.http) = { get: "/v1/items/{id}" };
    option (gnostic.openapi.v3.operation) = {summary: "Get an item"};
  }
}

message GetRequest {
  string id = 1 [(gnostic.openapi.v3.property) = {description: "the id"}, deprecated = true];
  int32 limit = 2 [(validate.rules).int32.gt = -1, (gnostic.openapi.v3.property).format = "int32"];
  string note = 3 [(gnostic.openapi.v3.property) = {title: 'it\'s'}];
  string tag = 4 [(.gnostic.openapi.v3.property) = {}];
}
`

func protoProject(t *testing.T) string {
	return project(t, map[string]string{
		"proto/shop/v1/shop.proto": shopProto,
		"vendor/x/x.proto":         shopProto,
		"main.go":                  "package main\n\nfunc main() {}\n",
	})
}

func TestProtoOptionsAndImports(t *testing.T) {
	var opts, http []codemod.ProtoOption
	var imports []codemod.ProtoImport
	var file *codemod.ProtoFile
	mods := []codemod.Codemod{{Version: "v0.9.0", Name: "look", Proto: func(f *codemod.ProtoFile) {
		file = f
		opts = f.Options("gnostic.openapi.v3")
		http = f.Options("google.api.http")
		imports = f.Imports()
		_ = f.Options("google.api") // a prefix matches whole name parts only
	}}}
	res, err := codemod.Run(protoProject(t), mods, nil)
	if err != nil || len(res.Changes) != 0 || len(res.Notes) != 0 {
		t.Fatalf("%+v, %v", res, err)
	}
	if file.Path != "proto/shop/v1/shop.proto" || file.Source() != shopProto {
		t.Fatalf("file = %s", file.Path)
	}

	if len(imports) != 2 || imports[0].Path != "google/api/annotations.proto" || imports[1].Path != "gnostic/openapi/v3/annotations.proto" {
		t.Fatalf("imports = %+v", imports)
	}
	if got := file.Text(imports[1].Start, imports[1].End); got != `import public "gnostic/openapi/v3/annotations.proto";` {
		t.Errorf("import statement = %q", got)
	}

	type seen struct {
		name, text, value string
		field             bool
	}
	want := []seen{
		{"gnostic.openapi.v3.document", "option (gnostic.openapi.v3.document) = {\n  info: { title: \"Shop; the API\" version: \"1\" } // a } in a comment\n};", "", false},
		{"gnostic.openapi.v3.operation", `option (gnostic.openapi.v3.operation) = {summary: "Get an item"};`, `{summary: "Get an item"}`, false},
		{"gnostic.openapi.v3.property", `(gnostic.openapi.v3.property) = {description: "the id"}`, `{description: "the id"}`, true},
		{"gnostic.openapi.v3.property", `(gnostic.openapi.v3.property).format = "int32"`, `"int32"`, true},
		{"gnostic.openapi.v3.property", `(gnostic.openapi.v3.property) = {title: 'it\'s'}`, `{title: 'it\'s'}`, true},
		{"gnostic.openapi.v3.property", `(.gnostic.openapi.v3.property) = {}`, `{}`, true},
	}
	if len(opts) != len(want) {
		t.Fatalf("options = %+v", opts)
	}
	for i, o := range opts {
		w := want[i]
		if o.Name != w.name || o.Field != w.field || file.Text(o.Start, o.End) != w.text {
			t.Errorf("option %d = %s %v %q, want %+v", i, o.Name, o.Field, file.Text(o.Start, o.End), w)
		}
		if w.value != "" && file.Text(o.ValueStart, o.ValueEnd) != w.value {
			t.Errorf("value %d = %q", i, file.Text(o.ValueStart, o.ValueEnd))
		}
	}
	if len(http) != 1 || file.Text(http[0].ValueStart, http[0].ValueEnd) != `{ get: "/v1/items/{id}" }` {
		t.Errorf("http = %+v", http)
	}
}

// gnosticToPlatform is the shape of a proto codemod: swap an import, rewrite options,
// report what it cannot.
var gnosticToPlatform = codemod.Codemod{Version: "v0.12.0", Name: "openapi.gnostic", Proto: func(f *codemod.ProtoFile) {
	for _, imp := range f.Imports() {
		if imp.Path == "gnostic/openapi/v3/annotations.proto" {
			f.Replace(imp.Start, imp.End, `import "platform/openapi/v1/annotations.proto";`)
		}
	}
	for _, o := range f.Options("gnostic.openapi.v3") {
		switch {
		case o.Name == "gnostic.openapi.v3.operation":
			f.Replace(o.Start, o.End, "option (platform.openapi.v1.operation) = "+f.Text(o.ValueStart, o.ValueEnd)+";")
		case o.Field:
			f.Report(o.Start, "move "+o.Name+" by hand")
		}
	}
}}

func TestProtoCodemod(t *testing.T) {
	dir := protoProject(t)
	mods := []codemod.Codemod{gnosticToPlatform}

	res, err := codemod.Run(dir, mods, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Changes) != 1 || res.Changes[0].Path != "proto/shop/v1/shop.proto" || res.Changes[0].Codemods[0] != "openapi.gnostic" {
		t.Fatalf("changes = %+v", res.Changes)
	}
	got := string(res.Changes[0].Content)
	for _, want := range []string{
		`import "platform/openapi/v1/annotations.proto"; // the docs`,
		`option (platform.openapi.v1.operation) = {summary: "Get an item"};`,
		`/* import "commented/out.proto"; */`,
		`option (google.api.http) = { get: "/v1/items/{id}" };`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("result lacks %q:\n%s", want, got)
		}
	}
	if len(res.Notes) != 4 || res.Notes[0].Line != 22 || res.Notes[1].Line != 23 || res.Notes[2].Line != 24 || res.Notes[3].Line != 25 ||
		res.Notes[0].String() != "proto/shop/v1/shop.proto:22: move gnostic.openapi.v3.property by hand (openapi.gnostic)" {
		t.Errorf("notes = %+v", res.Notes)
	}

	// Idempotent once written.
	if err := os.WriteFile(filepath.Join(dir, res.Changes[0].Path), res.Changes[0].Content, 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := codemod.Run(dir, mods, nil)
	if err != nil || len(again.Changes) != 0 {
		t.Errorf("second run: %+v, %v", again.Changes, err)
	}
}

func TestProtoCodemodErrors(t *testing.T) {
	overlap := codemod.Codemod{Version: "v0.9.0", Name: "overlap", Proto: func(f *codemod.ProtoFile) {
		f.Replace(0, 10, "a")
		f.Replace(5, 12, "b")
	}}
	if _, err := codemod.Run(protoProject(t), []codemod.Codemod{overlap}, nil); err == nil || !strings.Contains(err.Error(), "overlapping edits") {
		t.Errorf("err = %v", err)
	}

	same := codemod.Codemod{Version: "v0.9.0", Name: "same", Proto: func(f *codemod.ProtoFile) {
		f.Replace(0, 6, "syntax")
	}}
	if res, err := codemod.Run(protoProject(t), []codemod.Codemod{same}, nil); err != nil || len(res.Changes) != 0 {
		t.Errorf("an edit to the same text is a change: %+v, %v", res, err)
	}
}

func TestProtoLexerEdges(t *testing.T) {
	src := "option (a.b) = 1;\noption (a.b) = -\n// unterminated\n\"x\noption (a.b) = {\n/* unterminated"
	var opts []codemod.ProtoOption
	var imports []codemod.ProtoImport
	mods := []codemod.Codemod{{Version: "v0.9.0", Name: "edges", Proto: func(f *codemod.ProtoFile) {
		opts = f.Options("a.b")
		imports = f.Imports()
		f.Report(len(src)+10, "past the end")
	}}}
	dir := project(t, map[string]string{"a.proto": src + "\nimport \"x.proto\"\nimport weak;\n(x) = 1; [ (bad name) = 1 ]; option (a.b) ="})
	res, err := codemod.Run(dir, mods, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts) != 2 || opts[0].ValueEnd-opts[0].ValueStart != 1 {
		t.Errorf("options = %+v", opts)
	}
	if len(imports) != 0 {
		t.Errorf("imports = %+v", imports)
	}
	if len(res.Notes) != 1 || res.Notes[0].Line < 1 {
		t.Errorf("notes = %+v", res.Notes)
	}
}
