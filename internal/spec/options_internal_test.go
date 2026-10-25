package spec

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/aidarbn/platform-go/internal/registry"
)

// fake has the shapes PR-13 (sqlc) and PR-14 (buf, OpenAPI) are going to declare, so the
// mechanism is tested on them before any real module uses them.
var fake = registry.Module{Name: "fake", Options: []registry.Option{
	{Name: "proto_dir", Description: "proto files", Default: "proto"},
	{Name: "strict", Description: "a switch", Kind: registry.KindBool, Default: true},
	{Name: "workers", Description: "a count", Kind: registry.KindInt},
	{Name: "deps", Description: "buf deps", Kind: registry.KindList, Items: &registry.Option{Description: "a module"}},
	{Name: "openapi", Description: "documentation", Kind: registry.KindObject, Fields: []registry.Option{
		{Name: "generator", Description: "which generator", Enum: []string{"connect", "platform", "none"}, Default: "connect"},
	}},
	{Name: "sqlc", Description: "sqlc", Kind: registry.KindObject, Fields: []registry.Option{
		{Name: "out", Description: "output"},
		{Name: "overrides", Description: "type overrides", Kind: registry.KindList, Items: &registry.Option{
			Kind: registry.KindObject, Fields: []registry.Option{
				{Name: "db_type", Description: "database type", Required: true},
				{Name: "nullable", Description: "nullable column", Kind: registry.KindBool},
				{Name: "go_type", Description: "Go type", Required: true, OneOf: []registry.Option{
					{Description: "type name"},
					{Kind: registry.KindObject, Fields: []registry.Option{
						{Name: "type", Description: "type name", Required: true},
						{Name: "pointer", Description: "a pointer", Kind: registry.KindBool},
					}},
				}},
			},
		}},
		{Name: "renames", Description: "renames", Kind: registry.KindMap, Values: &registry.Option{Description: "new name"}},
	}},
}}

func TestFakeIsWellFormed(t *testing.T) {
	if err := registry.CheckOptions(fake); err != nil {
		t.Fatal(err)
	}
}

func check(t *testing.T, src string) []string {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, err := range checkOptions(fake, moduleNodes(&doc)["fake"]) {
		out = append(out, err.Error())
	}
	return out
}

const valid = `modules:
  fake:
    proto_dir: api/proto
    strict: false
    workers: 4
    deps: [buf.build/grpc-ecosystem/grpc-gateway]
    openapi: {generator: none}
    sqlc:
      out: internal/adapters/out/db/sqlcgen
      overrides:
        - {db_type: int4, go_type: int64}
        - {db_type: int4, nullable: true, go_type: {type: int64, pointer: true}}
      renames: {url: URL}
`

func TestTypedOptionsAccepted(t *testing.T) {
	if errs := check(t, valid); len(errs) > 0 {
		t.Fatal(errs)
	}
	if errs := check(t, "modules:\n  fake:\n"); len(errs) > 0 {
		t.Errorf("an empty section: %v", errs)
	}
	if errs := check(t, "modules:\n  fake: {}\n"); len(errs) > 0 {
		t.Errorf("{}: %v", errs)
	}
}

func TestTypedOptionErrors(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"number for a string", "proto_dir: 5", `line 3: module fake: option proto_dir must be a string, got an integer 5: quote it, "5"`},
		{"float for a string", "proto_dir: 1.27", `option proto_dir must be a string, got a number 1.27: quote it, "1.27"`},
		{"null", "proto_dir:", "option proto_dir must be a string, got nothing: remove the key to use the default"},
		{"string for a bool", "strict: \"yes\"", `option strict must be true or false, got a string "yes"`},
		{"bool for an int", "workers: true", "option workers must be an integer, got true"},
		{"scalar for a list", "deps: x", `option deps must be a list, got a string "x"`},
		{"bad element", "deps: [a, [b]]", "line 3: module fake: option deps[1] must be a string, got a list"},
		{"list for an object", "openapi: [x]", "option openapi must be an object, got a list"},
		{"enum", "openapi: {generator: swagger}", `option openapi.generator must be one of connect, platform, none, got "swagger"`},
		{"unknown key", "openapi: {generater: none}", `option openapi: unknown key "generater", known keys: generator`},
		{"unknown option", "proto: x", `line 3: module fake: unknown option "proto", known options: proto_dir, strict`},
		{"missing required", "sqlc: {overrides: [{db_type: int4}]}", "option sqlc.overrides[0]: missing required key go_type"},
		{"one_of mismatch", "sqlc: {overrides: [{db_type: int4, go_type: 5}]}", "option sqlc.overrides[0].go_type must be a string or an object, got an integer 5"},
		{"inside an alternative", "sqlc: {overrides: [{db_type: a, go_type: {type: b, pointr: true}}]}", `option sqlc.overrides[0].go_type: unknown key "pointr", known keys: type, pointer`},
		{"map value", "sqlc: {renames: {url: [x]}}", "option sqlc.renames.url must be a string, got a list"},
		{"map for a map", "sqlc: {renames: x}", `option sqlc.renames must be a map, got a string "x"`},
		{"merge key in a section", "<<: {strict: true}", "module fake: section: merge keys (<<) are not supported"},
		{"merge key in an object", "openapi: {<<: {generator: none}}", "option openapi: merge keys (<<) are not supported"},
		{"merge key in a map", "sqlc: {renames: {<<: {a: b}}}", "option sqlc.renames: merge keys (<<) are not supported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := check(t, "modules:\n  fake:\n    "+tc.src+"\n")
			if len(errs) != 1 || !strings.Contains(errs[0], tc.want) {
				t.Errorf("errs = %q\nwant %q", errs, tc.want)
			}
		})
	}
}

func TestEveryMistakeReported(t *testing.T) {
	errs := check(t, "modules:\n  fake:\n    strict: 1\n    workers: x\n    deps: [1, 2]\n")
	if len(errs) != 4 {
		t.Fatalf("errs = %q", errs)
	}
	if !strings.HasPrefix(errs[3], "line 5: ") {
		t.Errorf("the line of a list element: %q", errs[3])
	}
}

func TestAliasesAreFollowed(t *testing.T) {
	src := "x: &deps [a, b]\nbad: &bad 5\nmodules:\n  fake:\n    deps: *deps\n    proto_dir: *bad\n"
	errs := check(t, src)
	if len(errs) != 1 || !strings.Contains(errs[0], "option proto_dir must be a string") {
		t.Errorf("errs = %q", errs)
	}
	if got := moduleNodes(&yaml.Node{Kind: yaml.DocumentNode}); len(got) != 0 {
		t.Errorf("an empty document: %v", got)
	}
	var doc yaml.Node
	_ = yaml.Unmarshal([]byte("modules: [a]\n"), &doc)
	if got := moduleNodes(&doc); len(got) != 0 {
		t.Errorf("modules as a list: %v", got)
	}
}

func TestDescribeUnusualTag(t *testing.T) {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!binary", Value: "aGk="}
	if got := describe(n); got != "!!binary aGk=" {
		t.Errorf("describe = %q", got)
	}
	if shapeFits(registry.Kind(42), n) {
		t.Error("an unknown kind fits")
	}
	if got := article(registry.Kind(42)); got != "Kind(42)" {
		t.Errorf("article = %q", got)
	}
	var oe optionError
	if !errors.As(error(optionError{line: 1, module: "m", msg: "x"}), &oe) || oe.Error() != "line 1: module m: x" {
		t.Errorf("optionError = %v", oe)
	}
}

func TestOptionSchemaFragments(t *testing.T) {
	props := propertiesSchema(fake.Options)
	raw, err := json.Marshal(props)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"proto_dir":               `{"default":"proto","description":"proto files","type":"string"}`,
		"strict":                  `{"default":true,"description":"a switch","type":"boolean"}`,
		"workers":                 `{"description":"a count","type":"integer"}`,
		"deps":                    `{"description":"buf deps","items":{"description":"a module","type":"string"},"type":"array"}`,
		"openapi":                 `{"additionalProperties":false,"description":"documentation","properties":{"generator":{"default":"connect","description":"which generator","enum":["connect","platform","none"],"type":"string"}},"type":"object"}`,
		"sqlc.renames":            `{"additionalProperties":{"description":"new name","type":"string"},"description":"renames","type":"object"}`,
		"sqlc.overrides.go_type":  `{"description":"Go type","oneOf":[{"description":"type name","type":"string"},{"additionalProperties":false,"properties":{"pointer":{"description":"a pointer","type":"boolean"},"type":{"description":"type name","type":"string"}},"required":["type"],"type":"object"}]}`,
		"sqlc.overrides.required": `["db_type","go_type"]`,
	}
	pick := func(path string) any {
		parts := strings.Split(path, ".")
		v := got[parts[0]]
		for _, p := range parts[1:] {
			m := v.(map[string]any)
			switch p {
			case "overrides":
				v = m["properties"].(map[string]any)["overrides"].(map[string]any)["items"]
			case "required":
				v = m["required"]
			default:
				v = m["properties"].(map[string]any)[p]
			}
		}
		return v
	}
	for path, w := range want {
		var wv any
		if err := json.Unmarshal([]byte(w), &wv); err != nil {
			t.Fatal(err)
		}
		if g := pick(path); !reflect.DeepEqual(g, wv) {
			gj, _ := json.Marshal(g)
			t.Errorf("%s:\n got %s\nwant %s", path, gj, w)
		}
	}
}
