package gen_test

import (
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/gen"
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
