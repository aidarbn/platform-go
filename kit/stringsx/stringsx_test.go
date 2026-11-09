package stringsx

import (
	"testing"

	"github.com/aidarbn/platform-go/kit/stringsx/internal/testpb"
	"github.com/aidarbn/platform-go/kit/stringsx/stringspb"
)

func TestApplyRules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		in    string
		rules *stringspb.StringRules
		want  string
	}{
		{"trim leading and trailing", "  hello  ", &stringspb.StringRules{Trim: true}, "hello"},
		{"trim does not touch inner whitespace", "  a  b  ", &stringspb.StringRules{Trim: true}, "a  b"},
		{"whitespace only becomes empty after trim", "   ", &stringspb.StringRules{Trim: true}, ""},
		{"collapse inner spaces", "a  b   c", &stringspb.StringRules{CollapseSpaces: true}, "a b c"},
		{"collapse does not trim without trim", "  a  b  ", &stringspb.StringRules{CollapseSpaces: true}, " a b "},
		{"trim and collapse", "  a  b   c  ", &stringspb.StringRules{Trim: true, CollapseSpaces: true}, "a b c"},
		{"lowercase email", "  USER@Example.COM  ", &stringspb.StringRules{Trim: true, Lowercase: true}, "user@example.com"},
		{"uppercase country code", " kz ", &stringspb.StringRules{Trim: true, Uppercase: true}, "KZ"},
		{"lowercase wins over uppercase", "HeLLo", &stringspb.StringRules{Lowercase: true, Uppercase: true}, "hello"},
		{"strip control characters", "a\x00b\x01c", &stringspb.StringRules{StripControl: true}, "abc"},
		{"strip control keeps tab, newline, return", "a\tb\nc\rd", &stringspb.StringRules{StripControl: true}, "a\tb\nc\rd"},
		{"strip zero-width and BOM", "\ufeffhello\u200bworld", &stringspb.StringRules{StripControl: true}, "helloworld"},
		// "é" as NFD: "e" + combining acute (U+0065 U+0301).
		{"nfc composes", "café", &stringspb.StringRules{Nfc: true}, "café"},
		{"no rules, no change", "  Hello  ", &stringspb.StringRules{}, "  Hello  "},
		{
			"full pipeline", "  \ufeffHELLO  \u200bworld\x00  ",
			&stringspb.StringRules{StripControl: true, Trim: true, CollapseSpaces: true, Lowercase: true}, "hello world",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := apply(tc.in, tc.rules); got != tc.want {
				t.Fatalf("apply(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestStripControl(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"":             "",
		"plain":        "plain",
		"a\x00b":       "ab",
		"a\x7fb":       "ab", // DEL
		"line1\nline2": "line1\nline2",
		"\u200bhidden": "hidden",
		"\ufeffhead":   "head",
		"mix\x01\ttab": "mix\ttab",
	} {
		if got := stripControl(in); got != want {
			t.Errorf("stripControl(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCollapseSpaces(t *testing.T) {
	t.Parallel()

	for in, want := range map[string]string{
		"":             "",
		"abc":          "abc",
		"a b":          "a b",
		"a  b":         "a b",
		"a   b":        "a b",
		"a\tb":         "a b",
		"a\t\t\nb":     "a b",
		"  leading":    " leading",
		"trailing  ":   "trailing ",
		"  both  ends": " both ends",
	} {
		if got := collapseSpaces(in); got != want {
			t.Errorf("collapseSpaces(%q) = %q, want %q", in, got, want)
		}
	}
}

func ptr(s string) *string { return &s }

// TestApplyWalker runs the walker against a generated message: whitespace-only names
// collapse to "" (failing min_len in validation later, which is validation's job), NFD
// becomes NFC, unannotated fields stay as they are.
func TestApplyWalker(t *testing.T) {
	t.Parallel()

	t.Run("whitespace-only name becomes empty", func(t *testing.T) {
		t.Parallel()
		m := &testpb.Group{Name: "   \t  "}
		Apply(m)
		if m.Name != "" {
			t.Fatalf("name = %q", m.Name)
		}
	})
	t.Run("surrounding whitespace and runs collapsed", func(t *testing.T) {
		t.Parallel()
		m := &testpb.Group{Name: "  Капучино   большой  "}
		Apply(m)
		if m.Name != "Капучино большой" {
			t.Fatalf("name = %q", m.Name)
		}
	})
	t.Run("NFD is normalised to NFC", func(t *testing.T) {
		t.Parallel()
		m := &testpb.Group{Name: "café"}
		Apply(m)
		if m.Name != "café" {
			t.Fatalf("name = %q", m.Name)
		}
	})
	t.Run("unannotated fields are untouched", func(t *testing.T) {
		t.Parallel()
		m := &testpb.Group{Search: ptr("  unchanged  ")}
		Apply(m)
		if m.GetSearch() != "  unchanged  " {
			t.Fatalf("search = %q", m.GetSearch())
		}
	})
	t.Run("an optional field empty after the rules is cleared, an unset one stays unset", func(t *testing.T) {
		t.Parallel()
		m := &testpb.Group{Note: ptr("   ")}
		Apply(m)
		if m.Note != nil {
			t.Fatalf("note = %q, want cleared", *m.Note)
		}
		m = &testpb.Group{}
		Apply(m)
		if m.Note != nil {
			t.Fatal("an unset note stays unset")
		}
		m = &testpb.Group{Note: ptr("  x ")}
		Apply(m)
		if m.GetNote() != "x" {
			t.Fatalf("note = %q", m.GetNote())
		}
	})
	t.Run("lists, maps and nested messages", func(t *testing.T) {
		t.Parallel()
		m := &testpb.Group{
			Tags:     []string{" A ", "B "},
			Labels:   map[string]string{" k ": "  v "},
			Parent:   &testpb.Group{Name: " p "},
			Children: []*testpb.Group{{Name: " c1 "}, {Code: " kz "}},
			ByName:   map[string]*testpb.Group{"x": {Name: "  d  e "}},
		}
		Apply(m)
		if m.Tags[0] != "a" || m.Tags[1] != "b" {
			t.Errorf("tags = %q", m.Tags)
		}
		if m.Labels[" k "] != "v" {
			t.Errorf("labels = %q: values are trimmed, keys left alone", m.Labels)
		}
		if m.Parent.Name != "p" || m.Children[0].Name != "c1" || m.Children[1].Code != "KZ" {
			t.Errorf("nested = %v", m)
		}
		if m.ByName["x"].Name != "d e" {
			t.Errorf("message map value = %q", m.ByName["x"].Name)
		}
	})
	t.Run("strip_control only", func(t *testing.T) {
		t.Parallel()
		m := &testpb.Group{Raw: " a\x00b "}
		Apply(m)
		if m.Raw != " ab " {
			t.Fatalf("raw = %q", m.Raw)
		}
	})
	t.Run("nil is a no-op", func(t *testing.T) {
		t.Parallel()
		Apply(nil)
		var g *testpb.Group
		Apply(g)
	})
}
