package settingsx_test

import (
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/kit/settingsx"
)

func def(group, name string, kind settingsx.Kind, value string) settingsx.Definition {
	return settingsx.Definition{Key: group + "." + name, Group: group, Name: name, Kind: kind, Default: value}
}

func TestSchemaSortsAndGroups(t *testing.T) {
	schema, err := settingsx.NewSchema(
		def("orders.create", "timeout", settingsx.KindDuration, "30s"),
		def("api.ratelimit", "rps", settingsx.KindInt, "50"),
		def("orders.create", "max_attempts", settingsx.KindInt, "5"),
	)
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}

	var keys []string
	for _, d := range schema.Definitions() {
		keys = append(keys, d.Key)
	}
	want := "api.ratelimit.rps, orders.create.max_attempts, orders.create.timeout"
	if got := strings.Join(keys, ", "); got != want {
		t.Errorf("keys = %s", got)
	}

	groups := schema.Groups()
	if len(groups) != 2 || groups[0].Name != "api.ratelimit" || groups[1].Name != "orders.create" {
		t.Fatalf("groups = %+v", groups)
	}
	if len(groups[1].Definitions) != 2 {
		t.Errorf("orders.create = %+v", groups[1].Definitions)
	}
}

func TestSchemaRejectsBadDefinitions(t *testing.T) {
	cases := map[string]struct {
		def  settingsx.Definition
		want string
	}{
		"unknown type":    {def("a", "b", "colour", "red"), "unknown type"},
		"bad default":     {def("a", "b", settingsx.KindInt, "many"), "expected an integer"},
		"missing default": {def("a", "b", settingsx.KindBool, ""), "a default is required"},
		"key mismatch":    {settingsx.Definition{Key: "x", Group: "a", Name: "b", Kind: settingsx.KindBool, Default: "true"}, "key must be"},
		"no group":        {settingsx.Definition{Key: "a.b", Kind: settingsx.KindBool, Default: "true"}, "group and name are required"},
		"default below min": {settingsx.Definition{
			Key: "a.b", Group: "a", Name: "b", Kind: settingsx.KindInt, Default: "0", Min: "1",
		}, "below the minimum"},
		"bad bound": {settingsx.Definition{
			Key: "a.b", Group: "a", Name: "b", Kind: settingsx.KindInt, Default: "1", Max: "lots",
		}, "max: expected an integer"},
		"options on non string": {settingsx.Definition{
			Key: "a.b", Group: "a", Name: "b", Kind: settingsx.KindInt, Default: "1", Options: []string{"1"},
		}, "options are only allowed"},
		"bounds on string": {settingsx.Definition{
			Key: "a.b", Group: "a", Name: "b", Kind: settingsx.KindString, Default: "x", Min: "1",
		}, "min and max are only allowed"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := settingsx.NewSchema(tc.def)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSchemaRejectsDuplicateKey(t *testing.T) {
	_, err := settingsx.NewSchema(
		def("a", "b", settingsx.KindBool, "true"),
		def("a", "b", settingsx.KindBool, "false"),
	)
	if err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("err = %v", err)
	}
}

func TestSchemaReportsEveryProblemAtOnce(t *testing.T) {
	_, err := settingsx.NewSchema(
		def("a", "b", "colour", "red"),
		def("c", "d", settingsx.KindInt, "many"),
	)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "a.b") || !strings.Contains(err.Error(), "c.d") {
		t.Errorf("err = %v", err)
	}
}

func TestValidate(t *testing.T) {
	schema, err := settingsx.NewSchema(
		settingsx.Definition{Key: "a.rps", Group: "a", Name: "rps", Kind: settingsx.KindInt, Default: "50", Min: "1", Max: "100"},
		settingsx.Definition{Key: "a.timeout", Group: "a", Name: "timeout", Kind: settingsx.KindDuration, Default: "30s", Max: "1m"},
		settingsx.Definition{Key: "a.mode", Group: "a", Name: "mode", Kind: settingsx.KindString, Default: "fast", Options: []string{"fast", "slow"}},
		def("a", "schedule", settingsx.KindCron, "0 3 * * *"),
		def("a", "enabled", settingsx.KindBool, "true"),
	)
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}

	ok := map[string]string{
		"a.rps":      "100",
		"a.timeout":  "45s",
		"a.mode":     "slow",
		"a.schedule": "*/5 9-18 * * MON-FRI",
		"a.enabled":  "false",
	}
	for key, raw := range ok {
		if err := schema.Validate(key, raw); err != nil {
			t.Errorf("Validate(%s, %s) = %v", key, raw, err)
		}
	}

	bad := map[string]string{
		"a.rps":      "101",
		"a.timeout":  "2m",
		"a.mode":     "medium",
		"a.schedule": "every night",
		"a.enabled":  "maybe",
		"a.unknown":  "1",
	}
	for key, raw := range bad {
		if err := schema.Validate(key, raw); err == nil {
			t.Errorf("Validate(%s, %s) accepted a bad value", key, raw)
		}
	}
}

func TestCronValidation(t *testing.T) {
	schema := settingsx.MustSchema(def("a", "schedule", settingsx.KindCron, "@daily"))

	for _, raw := range []string{"@daily", "@HOURLY", "@every 1h30m", "0 3 * * *", "0 0 3 * * *", "*/15 * * * *"} {
		if err := schema.Validate("a.schedule", raw); err != nil {
			t.Errorf("%q rejected: %v", raw, err)
		}
	}
	for _, raw := range []string{"", "@never", "@every soon", "@every 0s", "0 3 * *", "0 3 * * * * *", "0 3 * * !"} {
		if err := schema.Validate("a.schedule", raw); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
}

func TestMustSchemaPanicsOnBadSchema(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("want a panic")
		}
	}()
	settingsx.MustSchema(def("a", "b", settingsx.KindInt, "many"))
}

func TestGroupOf(t *testing.T) {
	if got := settingsx.GroupOf("orders.create.timeout"); got != "orders.create" {
		t.Errorf("GroupOf = %q", got)
	}
	if got := settingsx.GroupOf("single"); got != "" {
		t.Errorf("GroupOf = %q", got)
	}
}
