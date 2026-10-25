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
		{"merged into a section", "<<: {strict: 1}", "option strict must be true or false, got an integer 1"},
		{"merged into an object", "openapi: {<<: {generater: none}}", `option openapi: unknown key "generater"`},
		{"merged into a map", "sqlc: {renames: {<<: {a: [b]}}}", "option sqlc.renames.a must be a string, got a list"},
		{"the earlier merge wins", "<<: [{proto_dir: 5}, {proto_dir: ok}]", "option proto_dir must be a string, got an integer 5"},
		{"timestamp", "proto_dir: 2026-10-26", `got a date 2026-10-26: quote it, "2026-10-26"`},
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

// Merge keys are read the way the decoder reads them: a key written in the mapping wins,
// then the earlier merged mapping.
func TestMergeKeysFollowTheDecoder(t *testing.T) {
	for _, src := range []string{
		"<<: {strict: true, proto_dir: x}",
		"proto_dir: ok\n    <<: {proto_dir: 5}",
		"<<: {proto_dir: 5}\n    proto_dir: ok",
		"<<: [{proto_dir: ok}, {proto_dir: 5}]",
		"openapi: {<<: {generator: none}}",
		"sqlc: {renames: {<<: {a: b}, c: d}}",
	} {
		if errs := check(t, "modules:\n  fake:\n    "+src+"\n"); len(errs) > 0 {
			t.Errorf("%s: %v", src, errs)
		}
	}
	// A merge source that is not a mapping is the decoder's error; here it adds nothing.
	if got := pairs(&yaml.Node{Kind: yaml.ScalarNode}); got != nil {
		t.Errorf("pairs of a scalar = %v", got)
	}
}

func TestAliasErrorNamesTheUse(t *testing.T) {
	errs := check(t, "x: &bad 5\nmodules:\n  fake:\n    strict: true\n    proto_dir: *bad\n")
	if len(errs) != 1 || !strings.HasPrefix(errs[0], "line 5: ") {
		t.Errorf("errs = %q", errs)
	}
}

var withRequired = registry.Module{Name: "req", Options: []registry.Option{
	{Name: "dir", Description: "a directory", Required: true},
}}

func TestRequiredOptionInAnEmptySection(t *testing.T) {
	for _, src := range []string{"modules:\n  req:\n", "modules:\n  req: {}\n", "s: &s {}\nmodules:\n  req: *s\n"} {
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
			t.Fatal(err)
		}
		errs := checkOptions(withRequired, moduleNodes(&doc)["req"])
		if len(errs) != 1 || !strings.Contains(errs[0].Error(), "module req: missing required option dir") {
			t.Errorf("%q: %v", src, errs)
		}
	}
	if errs := checkOptions(withRequired, nil); len(errs) != 1 {
		t.Errorf("no node at all: %v", errs)
	}
}

func TestWithDefaults(t *testing.T) {
	var section map[string]any
	if err := yaml.Unmarshal([]byte(`
proto_dir: ""
openapi: {}
sqlc:
  overrides:
    - {db_type: a, go_type: b}
    - {db_type: a, go_type: {type: c}}
  renames: {a: b}
`), &section); err != nil {
		t.Fatal(err)
	}
	opts := append([]registry.Option(nil), fake.Options...)
	// defaults deep inside: a field of the object alternative, a map value, a list element
	sqlc := opts[5]
	sqlc.Fields = append([]registry.Option(nil), sqlc.Fields...)
	items := *sqlc.Fields[1].Items
	items.Fields = append([]registry.Option(nil), items.Fields...)
	goType := items.Fields[2]
	alts := append([]registry.Option(nil), goType.OneOf...)
	obj := alts[1]
	obj.Fields = append([]registry.Option(nil), obj.Fields...)
	obj.Fields[1].Default = false
	alts[1] = obj
	goType.OneOf = alts
	items.Fields[2] = goType
	sqlc.Fields[1].Items = &items
	sqlc.Fields[2].Values = &registry.Option{Description: "new name", Default: "x"}
	sqlc.Fields = append(sqlc.Fields, registry.Option{Name: "tags", Description: "t", Kind: registry.KindList, Items: &registry.Option{Description: "t"}, Default: []any{"a"}})
	opts[5] = sqlc
	opts = append(opts, registry.Option{Name: "plain", Description: "no default"},
		registry.Option{Name: "pick", Description: "p", OneOf: []registry.Option{{}, {Kind: registry.KindBool}}, Default: "s"})

	got := withDefaults(opts, section)
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"openapi":{"generator":"connect"},"pick":"s","proto_dir":"proto","sqlc":{"overrides":[{"db_type":"a","go_type":"b"},{"db_type":"a","go_type":{"pointer":false,"type":"c"}}],"renames":{"a":"b"},"tags":["a"]},"strict":true}`
	if string(raw) != want {
		t.Errorf("withDefaults:\n got %s\nwant %s", raw, want)
	}
	if section["proto_dir"] != "" {
		t.Error("the section itself was changed")
	}
	// values of an unexpected shape are left to the validator
	if v := fillDefaults(opts[5], "x"); v != "x" {
		t.Errorf("object over a string = %v", v)
	}
	if v := fillDefaults(*opts[5].Fields[1].Items, map[string]any{}); v == nil {
		t.Error("an object element")
	}
	if v := fillDefaults(registry.Option{Kind: registry.KindList, Items: &registry.Option{}}, "x"); v != "x" {
		t.Errorf("list over a string = %v", v)
	}
	if v := fillDefaults(registry.Option{Kind: registry.KindMap, Values: &registry.Option{}}, "x"); v != "x" {
		t.Errorf("map over a string = %v", v)
	}
}
