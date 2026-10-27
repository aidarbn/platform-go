package gen_test

import (
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/gen"
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
		"features=google.api.http;protovalidate;gnostic",
		"with-proto-names",
		"out: api/openapi",
	} {
		if !strings.Contains(bufGen, want) {
			t.Errorf("buf.gen.yaml lacks %q:\n%s", want, bufGen)
		}
	}
	if !strings.Contains(string(files[gen.BufPath]), "buf.build/bufbuild/protovalidate") {
		t.Errorf("buf.yaml:\n%s", files[gen.BufPath])
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

// Without options buf.yaml and buf.gen.yaml are the files every project had before the
// options existed, byte for byte, with and without i18n.
func TestAPIDefaultsAreTheFilesBefore(t *testing.T) {
	for _, api := range []string{"  api: {}\n", "  api:\n    proto_dir: proto\n    buf: {deps: [], breaking_except: []}\n    openapi: {generator: connect}\n"} {
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
	if s, _ := gen.API(f); s.GeneratesOpenAPI() {
		t.Error("GeneratesOpenAPI with generator none")
	}
	// Everything but the OpenAPI plugin is the file of the connect generator.
	connect, err := gen.Wiring(mustParse(t, apiProject+"  api: {}\n  i18n: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	head, _, _ := strings.Cut(string(connect[gen.BufGenPath]), "  - local: [go, tool, protoc-gen-connect-openapi]")
	if !strings.HasPrefix(bufGen, head) || strings.Count(bufGen, "- local:") != 3 {
		t.Errorf("buf.gen.yaml with none:\n%s", bufGen)
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
		{"openapi: {generator: platform}", "openapi.generator: platform is not available in this version yet"},
		{"openapi: {generator: v2}", "must be one of connect, platform, none"},
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
