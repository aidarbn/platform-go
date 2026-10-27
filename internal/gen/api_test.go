package gen_test

import (
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/gen"
	"github.com/aidarbn/platform-go/internal/registry"
	"github.com/aidarbn/platform-go/internal/spec"
)

const withAPI = `
schema: 1
project:
  module: github.com/aidarbn/shop-api
modules:
  api: {}
`

func TestAPIFiles(t *testing.T) {
	files, err := gen.Wiring(mustParse(t, withAPI))
	if err != nil {
		t.Fatalf("Wiring: %v", err)
	}

	bufGen := string(files[gen.BufGenPath])
	for _, want := range []string{
		"value: github.com/aidarbn/shop-api/internal/api/gen", // go_package comes from managed mode
		"local: [go, tool, protoc-gen-go]",
		"local: [go, tool, protoc-gen-grpc-gateway]",
		"local: [go, tool, protoc-gen-platform-openapi]",
		"- base=api/openapi/base.yaml",
		"out: api/openapi",
	} {
		if !strings.Contains(bufGen, want) {
			t.Errorf("buf.gen.yaml lacks %q:\n%s", want, bufGen)
		}
	}
	if buf := string(files[gen.BufPath]); !strings.Contains(buf, "buf.build/bufbuild/protovalidate") || strings.Contains(buf, "gnostic") {
		t.Errorf("buf.yaml:\n%s", buf)
	}
	if _, ok := files["proto/"+gen.OpenAPIOptionsPath]; !ok {
		t.Error("the options of the generator are not written")
	}
	if !strings.Contains(string(files[gen.OpenAPIPath]), "//go:embed *") {
		t.Errorf("openapi.gen.go:\n%s", files[gen.OpenAPIPath])
	}

	modules := string(files[gen.ModulesPath])
	for _, want := range []string{
		`"github.com/aidarbn/shop-api/api/openapi"`,
		"api.New(cfg.API, api.WithOpenAPI(openapi.FS))",
	} {
		if !strings.Contains(modules, want) {
			t.Errorf("modules.gen.go lacks %q:\n%s", want, modules)
		}
	}

	plain, err := gen.Wiring(mustParse(t, withPostgres))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := plain[gen.BufGenPath]; ok {
		t.Error("buf files are generated without the api module")
	}
}

const apiProject = "schema: 1\nproject:\n  module: github.com/aidarbn/shop-api\nmodules:\n  postgres: {}\n"

// The default is the platform's generator: no gnostic, its plugin and options, the
// platform folder out of generation.
func TestAPIDefaults(t *testing.T) {
	for _, api := range []string{"  api: {}\n", "  api:\n    proto_dir: proto\n    buf: {deps: [], breaking_except: []}\n    openapi: {generator: platform}\n"} {
		files, err := gen.Wiring(mustParse(t, apiProject+api))
		if err != nil {
			t.Fatal(err)
		}
		golden(t, "buf.platform.yaml", files[gen.BufPath])
		golden(t, "buf.gen.platform.yaml", files[gen.BufGenPath])
		files, err = gen.Wiring(mustParse(t, apiProject+api+"  i18n: {}\n"))
		if err != nil {
			t.Fatal(err)
		}
		golden(t, "buf.platform.yaml", files[gen.BufPath])
		golden(t, "buf.gen.platform.yaml", files[gen.BufGenPath])
	}
}

// openapi.generator: connect gives buf.yaml and buf.gen.yaml every project had before
// v0.10.0, byte for byte, with and without i18n.
func TestAPIConnectIsTheFilesBefore(t *testing.T) {
	for _, api := range []string{"  api:\n    openapi: {generator: connect}\n", "  api:\n    proto_dir: proto\n    buf: {deps: [], breaking_except: []}\n    openapi: {generator: connect}\n"} {
		files, err := gen.Wiring(mustParse(t, apiProject+api))
		if err != nil {
			t.Fatal(err)
		}
		golden(t, "buf.default.yaml", files[gen.BufPath])
		golden(t, "buf.gen.default.yaml", files[gen.BufGenPath])

		files, err = gen.Wiring(mustParse(t, apiProject+api+"  i18n: {}\n"))
		if err != nil {
			t.Fatal(err)
		}
		golden(t, "buf.default.yaml", files[gen.BufPath])
		golden(t, "buf.gen.i18n.yaml", files[gen.BufGenPath])
		if _, ok := files[gen.I18nProtoPath]; !ok {
			t.Errorf("%s is not generated", gen.I18nProtoPath)
		}
	}
}

// taply's layout: the buf module is api/, grpc-gateway for its openapiv2 options and
// deletions allowed by breaking.
const taplyAPI = `  api:
    proto_dir: api
    buf:
      deps: [buf.build/grpc-ecosystem/grpc-gateway, buf.build/googleapis/googleapis]
      breaking_except: [FILE_NO_DELETE, RPC_NO_DELETE, MESSAGE_NO_DELETE, FIELD_NO_DELETE]
  i18n: {}
`

func TestAPITaplyLayout(t *testing.T) {
	f := mustParse(t, apiProject+taplyAPI)
	files, err := gen.Wiring(f)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "buf.taply.yaml", files[gen.BufPath])
	golden(t, "buf.gen.taply.yaml", files[gen.BufGenPath])
	if _, ok := files["api/platform/i18n/v1/i18n.proto"]; !ok {
		t.Error("the i18n option did not move under proto_dir")
	}
	if _, ok := files[gen.I18nProtoPath]; ok {
		t.Errorf("%s is still generated", gen.I18nProtoPath)
	}
	s, err := gen.API(f)
	if err != nil || s.ProtoDir != "api" || !s.GeneratesOpenAPI() {
		t.Errorf("settings = %+v, %v", s, err)
	}
}

// generator: none leaves the description to the project: no OpenAPI plugin, the embed
// of api/openapi stays.
func TestAPIWithoutOpenAPIGenerator(t *testing.T) {
	f := mustParse(t, apiProject+"  api:\n    openapi: {generator: none}\n  i18n: {}\n")
	files, err := gen.Wiring(f)
	if err != nil {
		t.Fatal(err)
	}
	bufGen := string(files[gen.BufGenPath])
	if strings.Contains(bufGen, "connect-openapi") || !strings.Contains(bufGen, "protoc-gen-grpc-gateway") || !strings.Contains(bufGen, "inputs:") {
		t.Errorf("buf.gen.yaml:\n%s", bufGen)
	}
	if _, ok := files[gen.OpenAPIPath]; !ok {
		t.Error("openapi.gen.go is gone: the project's own description would not be served")
	}
	if !strings.Contains(string(files[gen.BufPath]), "buf.build/gnostic/gnostic") || !strings.Contains(bufGen, "module: buf.build/gnostic/gnostic") {
		t.Errorf("none lost gnostic, which the project's annotations may need:\n%s", files[gen.BufPath])
	}
	if s, _ := gen.API(f); s.GeneratesOpenAPI() {
		t.Error("GeneratesOpenAPI with generator none")
	}
	// Everything but the OpenAPI plugin is the file of the connect generator, as before.
	connect, err := gen.Wiring(mustParse(t, apiProject+"  api:\n    openapi: {generator: connect}\n  i18n: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	head, _, _ := strings.Cut(string(connect[gen.BufGenPath]), "  - local: [go, tool, protoc-gen-connect-openapi]")
	if !strings.HasPrefix(bufGen, head) || strings.Count(bufGen, "- local:") != 3 || strings.Contains(bufGen, "platform-openapi") {
		t.Errorf("buf.gen.yaml with none:\n%s", bufGen)
	}
}

// generator: platform runs the platform's own plugin instead of connect, with the same
// output and base, and pins it in go.mod in place of the connect plugin.
func TestAPIPlatformOpenAPIGenerator(t *testing.T) {
	f := mustParse(t, apiProject+"  api:\n    openapi: {generator: platform}\n")
	files, err := gen.Wiring(f)
	if err != nil {
		t.Fatal(err)
	}
	bufGen := string(files[gen.BufGenPath])
	want := "  - local: [go, tool, protoc-gen-platform-openapi]\n    out: api/openapi\n    strategy: all\n"
	if strings.Contains(bufGen, "connect-openapi") || !strings.Contains(bufGen, want) || !strings.Contains(bufGen, "- base=api/openapi/base.yaml") {
		t.Errorf("buf.gen.yaml:\n%s", bufGen)
	}
	s, err := gen.API(f)
	if err != nil || !s.GeneratesOpenAPI() {
		t.Fatalf("settings = %+v, %v", s, err)
	}
	tools := []registry.Tool{{Package: "github.com/bufbuild/buf/cmd/buf"}, {Package: gen.ConnectOpenAPITool}}
	got := s.Tools(tools, "v1.2.3")
	if len(got) != 2 || got[0].Package != "github.com/bufbuild/buf/cmd/buf" || got[1] != (registry.Tool{Module: gen.PlatformModule, Package: gen.PlatformOpenAPITool, Version: "v1.2.3"}) {
		t.Errorf("tools = %+v", got)
	}
	s, _ = gen.API(mustParse(t, apiProject+"  api:\n    openapi: {generator: connect}\n"))
	if got := s.Tools(tools, "v1.2.3"); len(got) != 2 || got[1].Package != gen.ConnectOpenAPITool {
		t.Errorf("connect: tools = %+v", got)
	}
	s, _ = gen.API(mustParse(t, apiProject+"  api:\n    openapi: {generator: none}\n"))
	if got := s.Tools(tools, "v1.2.3"); len(got) != 1 {
		t.Errorf("none: tools = %+v", got)
	}
}

func TestAPIRejectsBadSettings(t *testing.T) {
	for _, tc := range []struct{ section, want string }{
		{"proto_dir: /abs", `proto_dir "/abs" must be a folder inside the project`},
		{"proto_dir: ../x", `proto_dir "../x" must be a folder inside the project`},
		{"proto_dir: .", `proto_dir "." must be a folder inside the project`},
		{"proto_dir: \"a: b\"", `must be a folder inside the project, of letters`},
		{"proto_dir: \"x #y\"", `must be a folder inside the project, of letters`},
		{"proto_dir: internal/api/gen/proto", `is inside a folder buf writes to`},
		{"proto_dir: api/openapi", `is inside a folder buf writes to`},
		{"buf: {deps: [buf.build/bufbuild/protovalidate:v1]}", "is a dependency of the platform"},
		{"buf: {deps: [grpc-gateway]}", `buf.deps: "grpc-gateway" is not a buf module`},
		{"buf: {deps: [\"buf.build/a/b\\nx\"]}", `is not a buf module`},
		{"buf: {deps: [buf.build/a/b, buf.build/a/b:v1]}", "buf.deps: buf.build/a/b is listed twice"},
		{"buf: {breaking_except: [field_no_delete]}", `"field_no_delete" is not a buf rule`},
		{"openapi: {generator: v2}", "must be one of platform, connect, none"},
		{"buf: {deps: [buf.build/gnostic/gnostic:v1]}\n    openapi: {generator: connect}", "is a dependency of the platform"},
		{"openapi: {generator: connect, documents: [{name: tablet}]}", "only the generator platform writes documents, not connect"},
		{"openapi: {generator: none, documents: [{name: tablet}]}", "only the generator platform writes documents, not none"},
		{"openapi: {documents: [{name: Tablet}]}", `name "Tablet" must be lower case`},
		{"openapi: {documents: [{name: all}]}", `name "all" is the whole description`},
		{"openapi: {documents: [{name: a}, {name: a}]}", `name "a" is used twice`},
		{"openapi: {documents: [{title: x}]}", "name"},
		{"openapi: {documents: [{name: a, include: {groups: [x]}}]}", "groups"},
	} {
		f, err := spec.Parse([]byte(apiProject + "  api:\n    " + tc.section + "\n"))
		if err == nil {
			_, err = gen.Wiring(f)
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.section, err, tc.want)
		}
	}
}

// Documents add one line to buf.gen.yaml, the plugin reads them from platformgo.yaml;
// without them buf.gen.yaml is the one of before.
func TestAPIOpenAPIDocuments(t *testing.T) {
	plain, err := gen.Wiring(mustParse(t, apiProject+"  api: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	empty, err := gen.Wiring(mustParse(t, apiProject+"  api:\n    openapi: {documents: []}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if string(plain[gen.BufGenPath]) != string(empty[gen.BufGenPath]) || strings.Contains(string(plain[gen.BufGenPath]), "documents=") {
		t.Errorf("buf.gen.yaml without documents:\n%s", plain[gen.BufGenPath])
	}
	f := mustParse(t, apiProject+`  api:
    openapi:
      documents:
        - name: tablet
          title: The tablet
          include: {tag_groups: [Tablet API], services: [shop.v1.TabletService]}
          exclude: {tags: [Debug]}
`)
	files, err := gen.Wiring(f)
	if err != nil {
		t.Fatal(err)
	}
	bufGen := string(files[gen.BufGenPath])
	if !strings.Contains(bufGen, "      - base=api/openapi/base.yaml\n      # The documents") || !strings.HasSuffix(strings.SplitN(bufGen, "inputs:", 2)[0], "      - documents=platformgo.yaml\n") {
		t.Errorf("buf.gen.yaml:\n%s", bufGen)
	}
	s, err := gen.API(f)
	if err != nil {
		t.Fatal(err)
	}
	d := s.OpenAPI.Documents
	if len(d) != 1 || d[0].Name != "tablet" || d[0].Title != "The tablet" || d[0].Include.TagGroups[0] != "Tablet API" ||
		d[0].Include.Services[0] != "shop.v1.TabletService" || d[0].Exclude.Tags[0] != "Debug" {
		t.Errorf("documents = %+v", d)
	}
}
