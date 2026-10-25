package registry_test

import (
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/registry"
)

func TestEveryModuleOptionsWellFormed(t *testing.T) {
	for _, m := range registry.All() {
		if err := registry.CheckOptions(m); err != nil {
			t.Errorf("module %s: %v", m.Name, err)
		}
	}
}

func str(name string) registry.Option {
	return registry.Option{Name: name, Description: "d"}
}

// The shapes the next CLI changes need — sqlc overrides, buf deps, a generator enum —
// are expressible and pass the check.
func TestCheckOptionsAcceptsNestedShapes(t *testing.T) {
	goType := registry.Option{Description: "Go type", OneOf: []registry.Option{
		{Kind: registry.KindString},
		{Kind: registry.KindObject, Fields: []registry.Option{
			{Name: "type", Description: "d", Required: true},
			{Name: "pointer", Description: "d", Kind: registry.KindBool, Default: false},
		}},
	}}
	goType.Name = "go_type"
	goType.Required = true
	m := registry.Module{Name: "x", Options: []registry.Option{
		{Name: "sqlc", Description: "d", Kind: registry.KindObject, Fields: []registry.Option{
			{Name: "dir", Description: "d", Default: "db"},
			{Name: "emit_interface", Description: "d", Kind: registry.KindBool, Default: true},
			{Name: "max", Description: "d", Kind: registry.KindInt, Default: 3},
			{Name: "overrides", Description: "d", Kind: registry.KindList, Default: []any{}, Items: &registry.Option{
				Kind: registry.KindObject, Fields: []registry.Option{str("db_type"), goType},
			}},
			{Name: "renames", Description: "d", Kind: registry.KindMap, Default: map[string]any{}, Values: &registry.Option{}},
		}},
		{Name: "generator", Description: "d", Enum: []string{"connect", "none"}, Default: "connect"},
	}}
	if err := registry.CheckOptions(m); err != nil {
		t.Fatal(err)
	}
	o, _ := m.Option("sqlc")
	if f, ok := o.Field("overrides"); !ok || f.Kind != registry.KindList {
		t.Errorf("Field(overrides) = %+v, %v", f, ok)
	}
	if _, ok := o.Field("nope"); ok {
		t.Error("an unknown field was found")
	}
	if got := strings.Join(registry.OptionNames(o.Fields), ","); got != "dir,emit_interface,max,overrides,renames" {
		t.Errorf("OptionNames = %s", got)
	}
}

func TestCheckOptionsRejectsMalformed(t *testing.T) {
	list := registry.KindList
	cases := []struct {
		name string
		opt  registry.Option
		want string
	}{
		{"no name", registry.Option{Description: "d"}, "an option without a name"},
		{"no description", registry.Option{Name: "a"}, "x.a: no description"},
		{"enum on bool", registry.Option{Name: "a", Description: "d", Kind: registry.KindBool, Enum: []string{"x"}}, "enum is allowed only on a string, not a bool"},
		{"default outside enum", registry.Option{Name: "a", Description: "d", Enum: []string{"x"}, Default: "y"}, `default "y" is not one of the enum`},
		{"default of wrong kind", registry.Option{Name: "a", Description: "d", Kind: registry.KindInt, Default: "1"}, "is not a int"},
		{"bool default of wrong kind", registry.Option{Name: "a", Description: "d", Kind: registry.KindBool, Default: 1}, "is not a bool"},
		{"list default of wrong kind", registry.Option{Name: "a", Description: "d", Kind: list, Items: &registry.Option{}, Default: "x"}, "is not a list"},
		{"map default of wrong kind", registry.Option{Name: "a", Description: "d", Kind: registry.KindMap, Values: &registry.Option{}, Default: []any{}}, "is not a map"},
		{"list without items", registry.Option{Name: "a", Description: "d", Kind: list}, "items go with a list"},
		{"items on a string", registry.Option{Name: "a", Description: "d", Items: &registry.Option{}}, "items go with a list"},
		{"object without fields", registry.Option{Name: "a", Description: "d", Kind: registry.KindObject}, "fields go with an object"},
		{"map without values", registry.Option{Name: "a", Description: "d", Kind: registry.KindMap}, "values go with a map"},
		{"unknown kind", registry.Option{Name: "a", Description: "d", Kind: registry.Kind(42)}, "unknown kind 42"},
		{"named element", registry.Option{Name: "a", Description: "d", Kind: list, Items: &registry.Option{Name: "e"}}, "x.a[]: an element has no name"},
		{"bad map value", registry.Option{Name: "a", Description: "d", Kind: registry.KindMap, Values: &registry.Option{Kind: list}}, "x.a{}: items go with a list"},
		{"one alternative", registry.Option{Name: "a", Description: "d", OneOf: []registry.Option{{}}}, "at least two alternatives"},
		{"one_of with kind", registry.Option{Name: "a", Description: "d", Kind: registry.KindBool, OneOf: []registry.Option{{}, {Kind: registry.KindBool}}}, "one_of excludes kind"},
		{"same kinds", registry.Option{Name: "a", Description: "d", OneOf: []registry.Option{{}, {}}}, "cannot be told apart"},
		{"named alternative", registry.Option{Name: "a", Description: "d", OneOf: []registry.Option{{Name: "n"}, {Kind: registry.KindBool}}}, "alternative 0: no name"},
		{"bad alternative", registry.Option{Name: "a", Description: "d", OneOf: []registry.Option{{}, {Kind: list}}}, "x.a|list: items go with a list"},
		{"object or map", registry.Option{Name: "a", Description: "d", OneOf: []registry.Option{{Kind: registry.KindObject, Fields: []registry.Option{str("f")}}, {Kind: registry.KindMap, Values: &registry.Option{}}}}, "an object and a map cannot be told apart"},
		{"one_of default", registry.Option{Name: "a", Description: "d", Default: 5, OneOf: []registry.Option{{}, {Kind: registry.KindBool}}}, "fits none of the alternatives"},
		{"bad field", registry.Option{Name: "a", Description: "d", Kind: registry.KindObject, Fields: []registry.Option{{Name: "f"}}}, "x.a.f: no description"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := registry.CheckOptions(registry.Module{Name: "x", Options: []registry.Option{tc.opt}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
		})
	}

	dup := registry.Module{Name: "x", Options: []registry.Option{str("a"), str("a")}}
	if err := registry.CheckOptions(dup); err == nil || !strings.Contains(err.Error(), "x.a: duplicate option") {
		t.Errorf("duplicate: %v", err)
	}
}

func TestKindString(t *testing.T) {
	want := map[registry.Kind]string{
		registry.KindString: "string", registry.KindBool: "bool", registry.KindInt: "int",
		registry.KindList: "list", registry.KindObject: "object", registry.KindMap: "map",
		registry.Kind(9): "Kind(9)",
	}
	for k, s := range want {
		if k.String() != s {
			t.Errorf("%d: %q, want %q", int(k), k.String(), s)
		}
	}
}
