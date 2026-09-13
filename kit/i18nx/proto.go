package i18nx

import (
	"fmt"
	"slices"
	"strings"

	"github.com/aidarbn/platform-go/kit/i18nx/i18npb"
)

// TranslationsToProto fills a field_translations map of a response.
func TranslationsToProto(t Translations) map[string]*i18npb.LocaleMap {
	if len(t) == 0 {
		return nil
	}
	out := make(map[string]*i18npb.LocaleMap, len(t))
	for field, locales := range t {
		values := make(map[string]string, len(locales))
		for locale, text := range locales {
			values[string(locale)] = text
		}
		out[field] = &i18npb.LocaleMap{Values: values}
	}
	return out
}

// ProtoToTranslations reads a field_translations map of a request.
func ProtoToTranslations(m map[string]*i18npb.LocaleMap) Translations {
	if len(m) == 0 {
		return nil
	}
	out := make(Translations, len(m))
	for field, lm := range m {
		if lm == nil || len(lm.GetValues()) == 0 {
			continue
		}
		values := make(map[Locale]string, len(lm.GetValues()))
		for locale, text := range lm.GetValues() {
			values[Locale(locale)] = text
		}
		out[field] = values
	}
	return out
}

// ValidateFields refuses a field_translations map with a field the entity does not
// translate, before it reaches the table.
func ValidateFields(m map[string]*i18npb.LocaleMap, allowed []string) error {
	for key := range m {
		if !slices.Contains(allowed, key) {
			sorted := slices.Clone(allowed)
			slices.Sort(sorted)
			return fmt.Errorf("field_translations: unknown field %q, allowed: [%s]", key, strings.Join(sorted, ", "))
		}
	}
	return nil
}
