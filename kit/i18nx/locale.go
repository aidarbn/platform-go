// Package i18nx translates the API, ported from taply's i18n package: translations of
// entity fields stored in the database and substituted into responses by
// Accept-Language, and static translations of error messages.
package i18nx

import (
	"context"
	"strings"

	"golang.org/x/text/language"
	"google.golang.org/grpc/metadata"
)

// Locale is a language code, such as ru or kk.
type Locale string

// Locales of taply.
const (
	LocaleRU Locale = "ru"
	LocaleKK Locale = "kk"
	LocaleEN Locale = "en"
	LocaleTR Locale = "tr"
	LocaleAZ Locale = "az"
	LocaleKY Locale = "ky"
)

// String returns the code.
func (l Locale) String() string { return string(l) }

// Tag returns the BCP 47 tag of the locale.
func (l Locale) Tag() language.Tag { return language.Make(string(l)) }

// LanguageInfo names a locale for language pickers.
type LanguageInfo struct {
	Code       Locale
	Name       string // in English
	NativeName string // in the language itself
}

// Languages are the locales with their names.
var Languages = []LanguageInfo{
	{Code: LocaleRU, Name: "Russian", NativeName: "Русский"},
	{Code: LocaleKK, Name: "Kazakh", NativeName: "Қазақша"},
	{Code: LocaleEN, Name: "English", NativeName: "English"},
	{Code: LocaleKY, Name: "Kyrgyz", NativeName: "Кыргызча"},
	{Code: LocaleAZ, Name: "Azerbaijani", NativeName: "Azərbaycan"},
	{Code: LocaleTR, Name: "Turkish", NativeName: "Türkçe"},
}

// LanguageByCode returns the names of a locale.
func LanguageByCode(code string) (LanguageInfo, bool) {
	for _, l := range Languages {
		if string(l.Code) == code {
			return l, true
		}
	}
	return LanguageInfo{}, false
}

type localeKey struct{}

// WithLocale returns a context that uses the locale instead of Accept-Language.
func WithLocale(ctx context.Context, locale Locale) context.Context {
	return context.WithValue(ctx, localeKey{}, locale)
}

// localeMatcher matches Accept-Language against the supported locales.
type localeMatcher struct {
	matcher  language.Matcher
	locales  []Locale
	fallback Locale
}

func newLocaleMatcher(locales []Locale, fallback Locale) *localeMatcher {
	tags := make([]language.Tag, len(locales))
	for i, l := range locales {
		tags[i] = l.Tag()
	}
	return &localeMatcher{matcher: language.NewMatcher(tags), locales: locales, fallback: fallback}
}

// match returns the best supported locale for a header, kk-KZ matching kk, and the
// fallback when nothing matches well.
func (m *localeMatcher) match(header string) Locale {
	tags, _, err := language.ParseAcceptLanguage(header)
	if err != nil {
		return m.fallback
	}
	filtered := tags[:0]
	for _, t := range tags {
		if t != language.Und && t.String() != "mul" {
			filtered = append(filtered, t)
		}
	}
	if len(filtered) == 0 {
		return m.fallback
	}
	_, index, confidence := m.matcher.Match(filtered...)
	if confidence < language.High {
		return m.fallback
	}
	return m.locales[index]
}

func acceptLanguage(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	if v := md.Get("accept-language"); len(v) > 0 {
		return strings.TrimSpace(v[0])
	}
	return ""
}
