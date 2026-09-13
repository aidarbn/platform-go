// Package settingsdef reads settings.yaml, the business settings schema of a project.
//
// The format is taply's configuration schema: a root of settings (or configs, as taply
// names it), groups that may nest, a _description per group, and per setting a type, a
// default, a description and requires_restart. On top of it come min, max, options and
// title. A taply schema is read as is.
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

// settingFields are the fields a setting may have.
var settingFields = []string{"type", "default", "min", "max", "options", "title", "description", "requires_restart"}

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
	var root map[string]any
	// An empty file is a valid schema: a project may enable the module before it has
	// any business settings.
	if err := yaml.NewDecoder(strings.NewReader(string(raw))).Decode(&root); err != nil && !errors.Is(err, io.EOF) {
		return settingsx.Schema{}, fmt.Errorf("%s: parse: %w", FileName, err)
	}

	body, err := rootNode(root)
	if err != nil {
		return settingsx.Schema{}, fmt.Errorf("%s: %w", FileName, err)
	}

	var (
		defs []settingsx.Definition
		errs []error
	)
	walk("", "", body, &defs, &errs)
	if len(errs) > 0 {
		return settingsx.Schema{}, fmt.Errorf("%s: %w", FileName, errors.Join(errs...))
	}

	schema, err := settingsx.NewSchema(defs...)
	if err != nil {
		return settingsx.Schema{}, fmt.Errorf("%s: %w", FileName, err)
	}
	return schema, nil
}

func rootNode(root map[string]any) (map[string]any, error) {
	var names []string
	for name := range root {
		names = append(names, name)
	}
	slices.Sort(names)

	switch {
	case len(root) == 0:
		return nil, nil
	case len(root) > 1 || (names[0] != "settings" && names[0] != "configs"):
		return nil, fmt.Errorf("expected one root, settings or configs, got %s", strings.Join(names, ", "))
	}
	value := root[names[0]]
	if value == nil {
		return nil, nil
	}
	body, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("parse: %s must be a mapping of groups", names[0])
	}
	return body, nil
}

// walk reads a group. A value with a type is a setting; any other mapping is a nested
// group whose name is joined with a dot.
func walk(group, description string, node map[string]any, defs *[]settingsx.Definition, errs *[]error) {
	if d, ok := node["_description"]; ok && group != "" {
		description = fmt.Sprint(d)
	}

	for _, name := range sortedKeys(node) {
		if name == "_description" {
			continue
		}
		child, ok := node[name].(map[string]any)
		if !ok {
			where := name
			if group != "" {
				where = group + "." + name
			}
			*errs = append(*errs, fmt.Errorf("%s: expected a setting or a group", where))
			continue
		}

		if _, isSetting := child["type"]; isSetting {
			if group == "" {
				*errs = append(*errs, fmt.Errorf("%s: a setting must be inside a group", name))
				continue
			}
			def, err := definition(group, name, description, child)
			if err != nil {
				*errs = append(*errs, err)
				continue
			}
			// Each setting is validated on its own, so one broken entry does not hide
			// the problems in the others.
			if _, err := settingsx.NewSchema(def); err != nil {
				*errs = append(*errs, err)
				continue
			}
			*defs = append(*defs, def)
			continue
		}

		if isSettingLike(child) {
			*errs = append(*errs, fmt.Errorf("%s: type is missing", join(group, name)))
			continue
		}
		walk(join(group, name), "", child, defs, errs)
	}
}

// isSettingLike tells a setting without its type from a nested group, so a forgotten
// type is reported instead of silently read as an empty group.
func isSettingLike(node map[string]any) bool {
	for key := range node {
		if key != "_description" && slices.Contains(settingFields, key) {
			return true
		}
	}
	return false
}

func join(group, name string) string {
	if group == "" {
		return name
	}
	return group + "." + name
}

func definition(group, name, groupDescription string, node map[string]any) (settingsx.Definition, error) {
	key := group + "." + name
	for field := range node {
		if !slices.Contains(settingFields, field) {
			return settingsx.Definition{}, fmt.Errorf("%s: field %s not found, known fields: %s", key, field, strings.Join(settingFields, ", "))
		}
	}

	def := settingsx.Definition{
		Key:              key,
		Group:            group,
		Name:             name,
		Kind:             settingsx.Kind(fmt.Sprint(node["type"])),
		GroupDescription: groupDescription,
	}

	for _, field := range []struct {
		name string
		out  *string
	}{
		{"default", &def.Default},
		{"min", &def.Min},
		{"max", &def.Max},
		{"title", &def.Title},
		{"description", &def.Description},
	} {
		text, err := scalar(node[field.name])
		if err != nil {
			return settingsx.Definition{}, fmt.Errorf("%s: %s: %w", key, field.name, err)
		}
		*field.out = text
	}

	if v, ok := node["requires_restart"]; ok {
		b, isBool := v.(bool)
		if !isBool {
			return settingsx.Definition{}, fmt.Errorf("%s: requires_restart: expected true or false", key)
		}
		def.RequiresRestart = b
	}

	if v, ok := node["options"]; ok {
		list, isList := v.([]any)
		if !isList {
			return settingsx.Definition{}, fmt.Errorf("%s: options: expected a list", key)
		}
		for _, item := range list {
			text, err := scalar(item)
			if err != nil {
				return settingsx.Definition{}, fmt.Errorf("%s: options: %w", key, err)
			}
			def.Options = append(def.Options, text)
		}
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
	case uint64:
		return strconv.FormatUint(t, 10), nil
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
