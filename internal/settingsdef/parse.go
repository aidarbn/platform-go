// Package settingsdef reads settings.yaml, the business settings schema of a project.
//
// The file is read by platformgo only: at runtime the project uses the generated Go
// code, so a typo in the schema is a generation error rather than a production one.
package settingsdef

import (
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/aidarbn/platform-go/kit/settingsx"
)

// FileName is the default schema file in the project root.
const FileName = "settings.yaml"

// file is the content of settings.yaml.
type file struct {
	Settings map[string]map[string]entry `yaml:"settings"`
}

// entry is one setting as written in the file.
type entry struct {
	Type    string   `yaml:"type"`
	Default any      `yaml:"default"`
	Min     any      `yaml:"min"`
	Max     any      `yaml:"max"`
	Options []string `yaml:"options"`
	Title   string   `yaml:"title"`
}

// Load reads the schema from a file.
func Load(path string) (settingsx.Schema, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return settingsx.Schema{}, fmt.Errorf("%s: %w", FileName, err)
	}
	return Parse(raw)
}

// Parse reads the schema from bytes.
func Parse(raw []byte) (settingsx.Schema, error) {
	var f file
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true) // a typo in a field name is an error, not a silently ignored key
	// An empty file is a valid schema: a project may enable the module before it has
	// any business settings.
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return settingsx.Schema{}, fmt.Errorf("%s: parse: %w", FileName, err)
	}

	var (
		defs []settingsx.Definition
		errs []error
	)
	for _, group := range sortedKeys(f.Settings) {
		for _, name := range sortedKeys(f.Settings[group]) {
			def, err := definition(group, name, f.Settings[group][name])
			if err != nil {
				errs = append(errs, err)
				continue
			}
			// Each setting is validated on its own, so one broken entry does not hide
			// the problems in the others.
			if _, err := settingsx.NewSchema(def); err != nil {
				errs = append(errs, err)
				continue
			}
			defs = append(defs, def)
		}
	}
	if len(errs) > 0 {
		return settingsx.Schema{}, fmt.Errorf("%s: %w", FileName, errors.Join(errs...))
	}

	schema, err := settingsx.NewSchema(defs...)
	if err != nil {
		return settingsx.Schema{}, fmt.Errorf("%s: %w", FileName, err)
	}
	return schema, nil
}

func definition(group, name string, e entry) (settingsx.Definition, error) {
	key := group + "." + name
	if e.Type == "" {
		return settingsx.Definition{}, fmt.Errorf("%s: type is missing", key)
	}

	def := settingsx.Definition{
		Key:     key,
		Group:   group,
		Name:    name,
		Kind:    settingsx.Kind(e.Type),
		Options: e.Options,
		Title:   e.Title,
	}

	for _, field := range []struct {
		name  string
		value any
		out   *string
	}{
		{"default", e.Default, &def.Default},
		{"min", e.Min, &def.Min},
		{"max", e.Max, &def.Max},
	} {
		text, err := scalar(field.value)
		if err != nil {
			return settingsx.Definition{}, fmt.Errorf("%s: %s: %w", key, field.name, err)
		}
		*field.out = text
	}
	return def, nil
}

// scalar turns a YAML value into text: values are stored and edited as text, and this
// is what keeps "30s", 30 and true written naturally in the file.
func scalar(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", nil
	case string:
		return t, nil
	case bool:
		return strconv.FormatBool(t), nil
	case int:
		return strconv.Itoa(t), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), nil
	default:
		return "", fmt.Errorf("expected a number, a string or true/false, got %T", v)
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
