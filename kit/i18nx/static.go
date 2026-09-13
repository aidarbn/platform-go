package i18nx

import (
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Prefixes of static translation keys, as in taply.
const (
	keyResource = "error.resource." // resource names in "{resource} {id}: {message}"
	keyField    = "error.field."    // field names in patterns
	keyPattern  = "error.pattern."  // "missing %s", "%s is required"
	keyTemplate = "error.tmpl."     // whole sentences with data slots
	keyMessage  = "error.msg."      // exact messages
)

// errorTemplate is a whole sentence: the English skeleton with %s slots matches the
// message, and the translation of the key renders it with the captured data.
type errorTemplate struct {
	key string
	en  string
}

// RegisterStatic registers a static translation in memory.
func (c *Client) RegisterStatic(key string, translations map[Locale]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.static[key] = translations

	// A template is matched by its English skeleton.
	if strings.HasPrefix(key, keyTemplate) && translations[LocaleEN] != "" {
		c.templates = slices.DeleteFunc(c.templates, func(t errorTemplate) bool { return t.key == key })
		c.templates = append(c.templates, errorTemplate{key: key, en: translations[LocaleEN]})
	}
}

// Static returns a static translation, falling back to the default locale and then to
// the key itself.
func (c *Client) Static(key string, locale Locale) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if text, ok := c.static[key][locale]; ok {
		return text
	}
	if text, ok := c.static[key][c.defaultLocale]; ok {
		return text
	}
	return key
}

// LoadMessages registers static translations from YAML, the format of i18n/messages.yaml:
//
//	error.msg.not found:
//	  en: not found
//	  ru: не найден
func (c *Client) LoadMessages(raw []byte) error {
	var messages map[string]map[string]string
	if err := yaml.Unmarshal(raw, &messages); err != nil {
		return fmt.Errorf("i18n: messages: %w", err)
	}
	var errs []string
	for key, locales := range messages {
		if len(locales) == 0 {
			errs = append(errs, key+": no translations")
			continue
		}
		tr := make(map[Locale]string, len(locales))
		for locale, text := range locales {
			tr[Locale(locale)] = text
		}
		if strings.HasPrefix(key, keyTemplate) {
			if tr[LocaleEN] == "" {
				errs = append(errs, key+": a template needs its English skeleton")
				continue
			}
			for locale, text := range tr {
				if strings.Count(text, "%s") != strings.Count(tr[LocaleEN], "%s") {
					errs = append(errs, fmt.Sprintf("%s: %s has a different number of %%s than en", key, locale))
				}
			}
		}
		c.RegisterStatic(key, tr)
	}
	if len(errs) > 0 {
		slices.Sort(errs)
		return fmt.Errorf("i18n: messages: %s", strings.Join(errs, "; "))
	}
	return nil
}
