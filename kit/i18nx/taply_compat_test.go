package i18nx_test

import (
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/kit/i18nx"
	"github.com/aidarbn/platform-go/kit/i18nx/i18npb"
)

func TestDefault(t *testing.T) {
	if got := i18nx.Default(); got != i18nx.LocaleRU {
		t.Fatalf("Default() = %q, want %q", got, i18nx.LocaleRU)
	}
}

func TestGetLanguageInfo(t *testing.T) {
	info, ok := i18nx.GetLanguageInfo("kk")
	if !ok || info.Code != i18nx.LocaleKK || info.NativeName != "Қазақша" {
		t.Fatalf("GetLanguageInfo(kk) = %+v, %v", info, ok)
	}
	if _, ok := i18nx.GetLanguageInfo("xx"); ok {
		t.Fatal("an unknown code is not found")
	}
	if len(i18nx.AllLanguages) != len(i18nx.Languages) {
		t.Fatal("AllLanguages is Languages")
	}
}

func TestNewWith(t *testing.T) {
	c := i18nx.NewWith(nil, i18nx.WithDefaultLocale(i18nx.LocaleEN), i18nx.WithLocales(i18nx.LocaleKK, i18nx.LocaleEN))
	if c.DefaultLocale() != i18nx.LocaleEN {
		t.Errorf("default = %s", c.DefaultLocale())
	}
	if got := c.Locales(); len(got) != 2 || got[0] != i18nx.LocaleKK || got[1] != i18nx.LocaleEN {
		t.Errorf("locales = %v", got)
	}
	if got := c.ResolveLocale(withLanguage("kk-KZ")); got != i18nx.LocaleKK {
		t.Errorf("kk-KZ resolves to %s", got)
	}
	if got := i18nx.NewWith(nil).DefaultLocale(); got != i18nx.LocaleRU {
		t.Errorf("no options: default = %s, want ru", got)
	}
}

func TestValidateProtoFields(t *testing.T) {
	allowed := []string{"name", "description"}
	ru := func(s string) *i18npb.LocaleMap { return &i18npb.LocaleMap{Values: map[string]string{"ru": s}} }

	for name, input := range map[string]map[string]*i18npb.LocaleMap{
		"nil":         nil,
		"empty":       {},
		"one allowed": {"name": ru("Маффин")},
		"all allowed": {"name": ru("Маффин"), "description": ru("Шоколадный")},
	} {
		if err := i18nx.ValidateProtoFields(input, allowed); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	for name, input := range map[string]map[string]*i18npb.LocaleMap{
		"unknown":            {"group_name": ru("Десерты")},
		"unknown and others": {"name": ru("Маффин"), "typo_field": ru("boo")},
	} {
		err := i18nx.ValidateProtoFields(input, allowed)
		if err == nil || !strings.Contains(err.Error(), "unknown field") ||
			!strings.Contains(err.Error(), "[description, name]") {
			t.Errorf("%s: err = %v, want the unknown field and the sorted allowed list", name, err)
		}
	}
}

// acceptLanguageSeeds are Accept-Language headers tablets and browsers send, and
// degenerate forms.
var acceptLanguageSeeds = []string{
	"", " ", "ru", "kk-KZ", "KK-kz", "en-US,en;q=0.9", "kk;q=0.9, ru;q=0.8", "*", "*, kk;q=0.5",
	"az, en, ru", "fr", "ru;q=abc", "fr;q=5, ru", "fr;q=-1, ru;q=0.1", "fr;q=NaN", "en;q=1e400",
	";q=1", ",,,", "en;;q=0.5;", "zh-Hant-TW;q=0.3", "\xff;q=0.5", "x-klingon",
}

// FuzzLocaleMatcher checks how a header is matched against the supported locales, the
// way ResolveLocale picks the language of a response. It must not panic and must always
// return one of the supported locales (the fallback on garbage), whatever the header.
func FuzzLocaleMatcher(f *testing.F) {
	for _, seed := range acceptLanguageSeeds {
		f.Add(seed)
	}

	supported := make([]i18nx.Locale, 0, len(i18nx.Languages))
	allowed := make(map[i18nx.Locale]bool, len(i18nx.Languages))
	for _, l := range i18nx.Languages {
		supported = append(supported, l.Code)
		allowed[l.Code] = true
	}
	c := i18nx.New(nil, i18nx.Default(), supported...)

	f.Fuzz(func(t *testing.T, header string) {
		if got := c.ResolveLocale(withLanguage(header)); !allowed[got] {
			t.Fatalf("ResolveLocale(%q) = %q: not a supported locale", header, got)
		}
	})
}
