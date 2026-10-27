package settingsx_test

import (
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/kit/settingsx"
)

func ofKind(kind settingsx.Kind) settingsx.Definition {
	return settingsx.Definition{Key: "g.k", Group: "g", Name: "k", Kind: kind}
}

// Every kind has its own idea of "the same value": a source that wrote "60s" or "t"
// must compare equal to a default of "1m" or "true".
func TestNormalize(t *testing.T) {
	cases := []struct {
		kind settingsx.Kind
		in   string
		want string
	}{
		{settingsx.KindBool, "true", "true"},
		{settingsx.KindBool, "t", "true"},
		{settingsx.KindBool, "TRUE", "true"},
		{settingsx.KindBool, "1", "true"},
		{settingsx.KindBool, "F", "false"},
		{settingsx.KindInt, "5", "5"},
		{settingsx.KindInt, "05", "5"},
		{settingsx.KindInt, "+5", "5"},
		{settingsx.KindInt, "-0", "0"},
		{settingsx.KindInt64, "9223372036854775807", "9223372036854775807"},
		{settingsx.KindInt64, "007", "7"},
		{settingsx.KindFloat, "0.95", "0.95"},
		{settingsx.KindFloat, "1.50", "1.5"},
		{settingsx.KindFloat, "1", "1"},
		{settingsx.KindFloat, "1.0", "1"},
		{settingsx.KindFloat, "1e3", "1000"},
		{settingsx.KindDuration, "1m", "1m0s"},
		{settingsx.KindDuration, "60s", "1m0s"},
		{settingsx.KindDuration, "1m0s", "1m0s"},
		{settingsx.KindDuration, "1500ms", "1.5s"},
		{settingsx.KindDuration, "0", "0s"},
		{settingsx.KindString, " fast ", " fast "},
		{settingsx.KindString, "", ""},
		{settingsx.KindCron, "0 3 * * *", "0 3 * * *"},
		{settingsx.KindCron, "  0  3 * *   * ", "0 3 * * *"},
		{settingsx.KindCron, "@DAILY", "@daily"},
		{settingsx.KindCron, "@every 60s", "@every 1m0s"},
		{settingsx.KindCron, "@Every  1m", "@every 1m0s"},
	}
	for _, c := range cases {
		got, err := settingsx.Normalize(ofKind(c.kind), c.in)
		if err != nil {
			t.Errorf("%s %q: %v", c.kind, c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s %q = %q, want %q", c.kind, c.in, got, c.want)
		}
	}
}

func TestNormalizeRejectsText(t *testing.T) {
	cases := []struct {
		kind settingsx.Kind
		in   string
		want string
	}{
		{settingsx.KindBool, "yes", "true or false"},
		{settingsx.KindInt, "5.0", "an integer"},
		{settingsx.KindInt, " 5", "an integer"},
		{settingsx.KindInt, "99999999999999999999", "an integer"},
		{settingsx.KindInt64, "x", "an integer"},
		{settingsx.KindFloat, "0,95", "a number"},
		{settingsx.KindDuration, "30", "a duration"},
		{settingsx.KindCron, "every day", "fields"},
		{settingsx.KindCron, "", "empty"},
		{"color", "red", "unknown type"},
	}
	for _, c := range cases {
		_, err := settingsx.Normalize(ofKind(c.kind), c.in)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %q: err = %v, want %q", c.kind, c.in, err, c.want)
		}
	}
}

// Normalize checks the kind only: bounds and options are Validate's business.
func TestNormalizeIgnoresBoundsAndOptions(t *testing.T) {
	d := settingsx.Definition{Key: "g.k", Group: "g", Name: "k", Kind: settingsx.KindInt, Max: "3"}
	if got, err := settingsx.Normalize(d, "10"); err != nil || got != "10" {
		t.Errorf("int above max = %q, %v", got, err)
	}
	d = settingsx.Definition{Key: "g.k", Group: "g", Name: "k", Kind: settingsx.KindString, Options: []string{"a"}}
	if got, err := settingsx.Normalize(d, "b"); err != nil || got != "b" {
		t.Errorf("string outside options = %q, %v", got, err)
	}
}

func TestEqual(t *testing.T) {
	cases := []struct {
		kind settingsx.Kind
		a, b string
		want bool
	}{
		{settingsx.KindBool, "true", "t", true},
		{settingsx.KindBool, "true", "false", false},
		{settingsx.KindInt, "5", "05", true},
		{settingsx.KindInt64, "5", "6", false},
		{settingsx.KindFloat, "0.95", "0.950", true},
		{settingsx.KindFloat, "0.95", "0.96", false},
		{settingsx.KindDuration, "1m", "60s", true},
		{settingsx.KindDuration, "1m", "61s", false},
		{settingsx.KindString, "fast", "fast", true},
		{settingsx.KindString, "fast", "Fast", false},
		{settingsx.KindCron, "0 3 * * *", "0  3 * * *", true},
		{settingsx.KindCron, "0 3 * * *", "0 4 * * *", false},
		// Values that do not parse equal only themselves.
		{settingsx.KindInt, "five", "five", true},
		{settingsx.KindInt, "five", "5", false},
		{settingsx.KindInt, "5", "five", false},
	}
	for _, c := range cases {
		if got := settingsx.Equal(ofKind(c.kind), c.a, c.b); got != c.want {
			t.Errorf("%s Equal(%q, %q) = %v", c.kind, c.a, c.b, got)
		}
	}
}
