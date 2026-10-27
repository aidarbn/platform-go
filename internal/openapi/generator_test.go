package openapi_test

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bufbuild/protocompile"
	"github.com/bufbuild/protocompile/linker"
	"github.com/bufbuild/protocompile/protoutil"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/aidarbn/platform-go/internal/openapi"
	"github.com/aidarbn/platform-go/internal/openapi/oascheck"

	// The files the test protos import, resolved from the binary.
	_ "google.golang.org/genproto/googleapis/api/httpbody"
	_ "google.golang.org/protobuf/types/known/durationpb"
	_ "google.golang.org/protobuf/types/known/emptypb"
	_ "google.golang.org/protobuf/types/known/fieldmaskpb"
	_ "google.golang.org/protobuf/types/known/timestamppb"
	_ "google.golang.org/protobuf/types/known/wrapperspb"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// compile compiles proto files of testdata/proto with their comments; imports outside
// of it come from the descriptors compiled into the test binary.
func compile(t *testing.T, root string, names ...string) []*descriptorpb.FileDescriptorProto {
	t.Helper()
	c := protocompile.Compiler{
		Resolver: protocompile.CompositeResolver{
			&protocompile.SourceResolver{ImportPaths: []string{root}},
			protocompile.ResolverFunc(func(path string) (protocompile.SearchResult, error) {
				fd, err := protoregistry.GlobalFiles.FindFileByPath(path)
				if err != nil {
					return protocompile.SearchResult{}, err
				}
				return protocompile.SearchResult{Desc: fd}, nil
			}),
		},
		SourceInfoMode: protocompile.SourceInfoStandard,
	}
	files, err := c.Compile(context.Background(), names...)
	if err != nil {
		t.Fatal(err)
	}
	var out []*descriptorpb.FileDescriptorProto
	seen := map[string]bool{}
	var add func(f protoreflect.FileDescriptor)
	add = func(f protoreflect.FileDescriptor) {
		if seen[f.Path()] {
			return
		}
		seen[f.Path()] = true
		for i := range f.Imports().Len() {
			add(f.Imports().Get(i).FileDescriptor)
		}
		if _, ok := f.(linker.File); ok {
			out = append(out, protoutil.ProtoFromFileDescriptor(f))
		} else {
			out = append(out, protodesc.ToFileDescriptorProto(f))
		}
	}
	for _, f := range files {
		add(f)
	}
	return out
}

// run sends the request the way buf does, as bytes, so the options are decoded by the
// plugin the way they are in a real run.
func run(t *testing.T, param string, generate []string, files []*descriptorpb.FileDescriptorProto) *pluginpb.CodeGeneratorResponse {
	t.Helper()
	in, err := proto.Marshal(&pluginpb.CodeGeneratorRequest{Parameter: proto.String(param), FileToGenerate: generate, ProtoFile: files})
	if err != nil {
		t.Fatal(err)
	}
	out, err := openapi.Run(in)
	if err != nil {
		t.Fatal(err)
	}
	resp := &pluginpb.CodeGeneratorResponse{}
	if err := proto.Unmarshal(out, resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v; run go test ./internal/openapi -update", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the generated description; run go test ./internal/openapi -update and review the diff", path)
	}
}

var shopFiles = []string{"shop/v1/shop.proto", "shop/v1/types.proto", "legacy/v1/legacy.proto"}

func TestGoldenShop(t *testing.T) {
	files := compile(t, "testdata/proto", shopFiles...)
	for _, c := range []struct{ name, param string }{
		{"shop.yaml", "layout=single,path=shop.yaml"},
		{"shop-base.yaml", "base=testdata/base.yaml,layout=single,path=shop-base.yaml"},
	} {
		resp := run(t, c.param, shopFiles, files)
		if resp.GetError() != "" {
			t.Fatal(resp.GetError())
		}
		if len(resp.GetFile()) != 1 {
			t.Fatalf("files = %d", len(resp.GetFile()))
		}
		got := []byte(resp.GetFile()[0].GetContent())
		if err := oascheck.Validate(got); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		golden(t, c.name, got)
		if again := run(t, c.param, shopFiles, files); again.GetFile()[0].GetContent() != string(got) {
			t.Errorf("%s: a second run gives other bytes", c.name)
		}
	}
	if resp := run(t, "", shopFiles, files); resp.GetSupportedFeatures()&uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL) == 0 {
		t.Error("proto3 optional is not declared supported")
	}
}

// Every protovalidate rule the generator knows, the custom verbs and the well-known
// types on the request side.
func TestGoldenRules(t *testing.T) {
	files := compile(t, "testdata/proto", "rules/v1/rules.proto")
	resp := run(t, "layout=single,path=rules.yaml", []string{"rules/v1/rules.proto"}, files)
	if resp.GetError() != "" {
		t.Fatal(resp.GetError())
	}
	got := []byte(resp.GetFile()[0].GetContent())
	if err := oascheck.Validate(got); err != nil {
		t.Fatal(err)
	}
	golden(t, "rules.yaml", got)
}

// The platform's options: sections and their groups, a summary, hidden methods and
// services, examples.
func TestGoldenOptions(t *testing.T) {
	files := compile(t, "testdata/proto", "opts/v1/opts.proto")
	resp := run(t, "layout=single,path=opts.yaml", []string{"opts/v1/opts.proto"}, files)
	if resp.GetError() != "" {
		t.Fatal(resp.GetError())
	}
	got := []byte(resp.GetFile()[0].GetContent())
	if err := oascheck.Validate(got); err != nil {
		t.Fatal(err)
	}
	golden(t, "opts.yaml", got)
	for _, hidden := range []string{"/v1/ping", "/debug", "InternalService"} {
		if bytes.Contains(got, []byte(hidden)) {
			t.Errorf("%s is described", hidden)
		}
	}
	// Groups the base lists itself win over the generated ones.
	base := filepath.Join(t.TempDir(), "base.yaml")
	if err := os.WriteFile(base, []byte("openapi: 3.1.0\ninfo: {title: t, version: \"1\"}\nx-tagGroups: [{name: All, tags: [Menu]}]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp = run(t, "layout=single,base="+base, []string{"opts/v1/opts.proto"}, files)
	if out := resp.GetFile()[0].GetContent(); !strings.Contains(out, "name: All") || strings.Contains(out, "Public API") {
		t.Errorf("the groups of the base were replaced:\n%s", out)
	}
	// Without groups of its own, a tag only the base has gets a group of its name.
	if err := os.WriteFile(base, []byte("openapi: 3.1.0\ninfo: {title: t, version: \"1\"}\ntags: [{name: Guides, description: How to}]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp = run(t, "layout=single,base="+base, []string{"opts/v1/opts.proto"}, files)
	if out := resp.GetFile()[0].GetContent(); !strings.Contains(out, "  - name: Guides\n    tags:\n      - Guides\n") {
		t.Errorf("the tag of the base is in no group:\n%s", out)
	}
}

// A binding the generator cannot describe fails the generation with the method named.
func TestBadBindings(t *testing.T) {
	for name, want := range map[string]string{
		"twice.proto": "GET /v1/{id} is bound twice: bad.v1.S.A and bad.v1.S.B; OpenAPI has one operation for a method and a path, so give the paths different literal segments",
		"path.proto":  "bad.v1.S.A: path /v1/{missing}: bad.v1.M has no field missing",
		"body.proto":  "bad.v1.S.A: body missing: bad.v1.M has no such field",
		"brace.proto": `bad.v1.S.A: path "/v1/{id": unclosed {`,
	} {
		files := compile(t, "testdata/bad", name)
		if resp := run(t, "", []string{name}, files); resp.GetError() != want {
			t.Errorf("%s: error = %q, want %q", name, resp.GetError(), want)
		}
	}
}

func TestErrorsGoIntoTheResponse(t *testing.T) {
	files := compile(t, "testdata/proto", shopFiles...)
	for param, want := range map[string]string{
		"color=red":        `unknown option "color"`,
		"layout=tree":      `layout "tree": split or single`,
		"base=testdata":    "testdata",
		"base=missing.yml": "base: open missing.yml",
	} {
		resp := run(t, param, shopFiles, files)
		if !strings.Contains(resp.GetError(), want) || (want == "") != (resp.GetError() == "") {
			t.Errorf("%s: error = %q, want %q", param, resp.GetError(), want)
		}
	}
	if _, err := openapi.Run([]byte("not a request")); err == nil {
		t.Error("garbage is read as a request")
	}
	if resp := run(t, "", []string{"missing.proto"}, files); resp.GetError() == "" {
		t.Error("a file to generate that is not in the request is not an error")
	}
	last := files[len(files)-1:]
	if resp := run(t, "", []string{last[0].GetName()}, last); resp.GetError() == "" {
		t.Error("a request without the imports of its files is not an error")
	}
}
