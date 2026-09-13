package settingsdef_test

import (
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/settingsdef"
	"github.com/aidarbn/platform-go/kit/settingsx"
)

const example = `
settings:
  orders.cleanup:
    enabled:  { type: bool, default: true }
    schedule: { type: cron, default: "0 3 * * *", title: Cleanup schedule }
  orders.create:
    max_attempts: { type: int, default: 5, min: 1, max: 20 }
    timeout:      { type: duration, default: 30s }
    mode:         { type: string, default: fast, options: [fast, slow] }
`

func TestParseExample(t *testing.T) {
	schema, err := settingsdef.Parse([]byte(example))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	defs := schema.Definitions()
	if len(defs) != 5 {
		t.Fatalf("definitions = %d", len(defs))
	}

	// Keys are sorted, so the generated code does not change between runs.
	var keys []string
	for _, d := range defs {
		keys = append(keys, d.Key)
	}
	want := "orders.cleanup.enabled, orders.cleanup.schedule, orders.create.max_attempts, orders.create.mode, orders.create.timeout"
	if got := strings.Join(keys, ", "); got != want {
		t.Errorf("keys = %s", got)
	}

	byKey := func(key string) settingsx.Definition {
		t.Helper()
		d, ok := schema.Definition(key)
		if !ok {
			t.Fatalf("%s is missing", key)
		}
		return d
	}

	// A YAML bool, a number and a bare duration all become text.
	if d := byKey("orders.cleanup.enabled"); d.Kind != settingsx.KindBool || d.Default != "true" {
		t.Errorf("enabled = %+v", d)
	}
	if d := byKey("orders.create.max_attempts"); d.Default != "5" || d.Min != "1" || d.Max != "20" {
		t.Errorf("max_attempts = %+v", d)
	}
	if d := byKey("orders.create.timeout"); d.Kind != settingsx.KindDuration || d.Default != "30s" {
		t.Errorf("timeout = %+v", d)
	}
	if d := byKey("orders.create.mode"); len(d.Options) != 2 || d.Options[0] != "fast" {
		t.Errorf("mode = %+v", d)
	}
	if d := byKey("orders.cleanup.schedule"); d.Title != "Cleanup schedule" || d.Group != "orders.cleanup" || d.Name != "schedule" {
		t.Errorf("schedule = %+v", d)
	}
}

func TestParseEmptyFile(t *testing.T) {
	for _, raw := range []string{"", "settings: {}\n"} {
		schema, err := settingsdef.Parse([]byte(raw))
		if err != nil {
			t.Fatalf("Parse(%q): %v", raw, err)
		}
		if len(schema.Definitions()) != 0 {
			t.Errorf("Parse(%q) = %+v", raw, schema.Definitions())
		}
	}
}

func TestParseRejectsBadSchema(t *testing.T) {
	cases := map[string]struct {
		raw  string
		want string
	}{
		"unknown field": {
			"settings:\n  a:\n    b: { type: bool, default: true, colour: red }\n",
			"field colour not found",
		},
		"missing type": {
			"settings:\n  a:\n    b: { default: true }\n",
			"a.b: type is missing",
		},
		"unknown type": {
			"settings:\n  a:\n    b: { type: colour, default: red }\n",
			"unknown type",
		},
		"bad default": {
			"settings:\n  a:\n    b: { type: int, default: many }\n",
			"a.b: default: expected an integer",
		},
		"default out of bounds": {
			"settings:\n  a:\n    b: { type: int, default: 0, min: 1 }\n",
			"below the minimum",
		},
		"bad cron": {
			"settings:\n  a:\n    b: { type: cron, default: nightly }\n",
			"a.b: default",
		},
		"structured default": {
			"settings:\n  a:\n    b:\n      type: string\n      default: [a, b]\n",
			"a.b: default: expected a number, a string or true/false",
		},
		"not a mapping": {
			"settings: [a, b]\n",
			"parse",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := settingsdef.Parse([]byte(tc.raw))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if err != nil && !strings.Contains(err.Error(), settingsdef.FileName) {
				t.Errorf("the error must name the file: %v", err)
			}
		})
	}
}

func TestParseReportsEveryProblemAtOnce(t *testing.T) {
	_, err := settingsdef.Parse([]byte(
		"settings:\n  a:\n    b: { type: int, default: many }\n    c: { default: 1 }\n"))
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "a.b") || !strings.Contains(err.Error(), "a.c") {
		t.Errorf("err = %v", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := settingsdef.Load(t.TempDir() + "/nope.yaml"); err == nil {
		t.Fatal("want an error")
	}
}

// The configuration schema of taply is read as is: the configs root, a description per
// group and per setting, requires_restart, nested groups and every taply type.
const taplyFormat = `
configs:
  sync:
    _description: "Menu synchronisation with external systems"

    queue_workers:
      type: int
      default: 3
      description: "Workers of the sync queue"
      requires_restart: true

    enabled:
      type: bool
      default: true
      description: "Run the sync worker"
      requires_restart: false

    timeout:
      type: duration
      default: "2h"
      description: "Timeout of one restaurant"

  sync_all:
    _description: "Periodic full sync"
    schedule:
      type: cron
      default: "*/15 * * * *"
      description: "Cron schedule"

  payments:
    kaspi:
      _description: "Kaspi payments"
      max_amount:
        type: int64
        default: 5000000000
      fee_percent:
        type: float
        default: 0.95
        min: 0
        max: 100
`

func TestParseTaplyFormat(t *testing.T) {
	schema, err := settingsdef.Parse([]byte(taplyFormat))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	get := func(key string) settingsx.Definition {
		t.Helper()
		d, ok := schema.Definition(key)
		if !ok {
			t.Fatalf("%s is missing: %v", key, schema.Definitions())
		}
		return d
	}

	workers := get("sync.queue_workers")
	if !workers.RequiresRestart || workers.Description != "Workers of the sync queue" || workers.GroupDescription != "Menu synchronisation with external systems" {
		t.Errorf("queue_workers = %+v", workers)
	}
	if get("sync.enabled").RequiresRestart {
		t.Error("requires_restart: false was read as true")
	}
	if d := get("sync_all.schedule"); d.Kind != settingsx.KindCron || d.Default != "*/15 * * * *" {
		t.Errorf("schedule = %+v", d)
	}

	// A nested group joins its names with a dot and keeps its own description.
	if d := get("payments.kaspi.max_amount"); d.Kind != settingsx.KindInt64 || d.Default != "5000000000" || d.GroupDescription != "Kaspi payments" {
		t.Errorf("max_amount = %+v", d)
	}
	if d := get("payments.kaspi.fee_percent"); d.Kind != settingsx.KindFloat || d.Default != "0.95" || d.Max != "100" {
		t.Errorf("fee_percent = %+v", d)
	}

	groups := schema.Groups()
	var names []string
	for _, g := range groups {
		names = append(names, g.Name+"="+g.Description)
	}
	want := "payments.kaspi=Kaspi payments, sync=Menu synchronisation with external systems, sync_all=Periodic full sync"
	if got := strings.Join(names, ", "); got != want {
		t.Errorf("groups = %s", got)
	}
}

func TestParseRejectsTaplyFormatMistakes(t *testing.T) {
	cases := map[string]struct {
		raw  string
		want string
	}{
		"two roots":           {"settings: {}\nconfigs: {}\n", "expected one root"},
		"unknown root":        {"options: {}\n", "expected one root"},
		"setting at the root": {"configs:\n  enabled: { type: bool, default: true }\n", "must be inside a group"},
		"restart not a bool":  {"configs:\n  a:\n    b: { type: bool, default: true, requires_restart: sometimes }\n", "requires_restart"},
		"float below min":     {"configs:\n  a:\n    b: { type: float, default: -1.5, min: 0 }\n", "below the minimum"},
		"int64 not a number":  {"configs:\n  a:\n    b: { type: int64, default: lots }\n", "expected an integer"},
		"scalar in a group":   {"configs:\n  a:\n    b: 5\n", "a.b: expected a setting or a group"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := settingsdef.Parse([]byte(tc.raw))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
