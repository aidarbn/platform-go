package spec

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/aidarbn/platform-go/internal/registry"
)

// optionError is a mistake in a module section, with the line it is on.
type optionError struct {
	line   int
	module string
	msg    string
}

func (e optionError) Error() string {
	return fmt.Sprintf("line %d: module %s: %s", e.line, e.module, e.msg)
}

// moduleNodes finds the section of every module in the parsed document, so options are
// checked against the YAML itself: the kind of a scalar is known exactly ("5" is a
// string, 5 is not) and errors name the line.
func moduleNodes(doc *yaml.Node) map[string]*yaml.Node {
	out := map[string]*yaml.Node{}
	root := resolve(doc)
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = resolve(root.Content[0])
	}
	modules := mappingValue(root, "modules")
	if modules == nil || modules.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(modules.Content); i += 2 {
		out[modules.Content[i].Value] = resolve(modules.Content[i+1])
	}
	return out
}

func mappingValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return resolve(n.Content[i+1])
		}
	}
	return nil
}

// resolve follows an alias to the node it names.
func resolve(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

// checkOptions rejects an option the module does not have and a value of the wrong
// type: a typo in platformgo.yaml must fail rather than be ignored.
func checkOptions(m registry.Module, section *yaml.Node) []error {
	if section == nil || section.Kind != yaml.MappingNode {
		return nil // an empty section; anything else does not decode into File
	}
	c := checker{module: m.Name}
	c.mapping(section, "", m.Options, "option", "options")
	return c.errs
}

type checker struct {
	module string
	errs   []error
}

func (c *checker) fail(n *yaml.Node, format string, args ...any) {
	c.errs = append(c.errs, optionError{line: n.Line, module: c.module, msg: fmt.Sprintf(format, args...)})
}

// mapping checks the keys of a section or an object against the declared fields. word
// names a key in messages: "option" at the top of a section, "key" inside an object.
func (c *checker) mapping(n *yaml.Node, path string, fields []registry.Option, word, words string) {
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := n.Content[i], resolve(n.Content[i+1])
		if key.ShortTag() == "!!merge" {
			c.fail(key, "%s: merge keys (<<) are not supported, write the keys out", where(path))
			continue
		}
		o, ok := findField(fields, key.Value)
		if !ok {
			known := "none"
			if len(fields) > 0 {
				known = strings.Join(registry.OptionNames(fields), ", ")
			}
			if path == "" {
				c.fail(key, "unknown %s %q, known %s: %s", word, key.Value, words, known)
			} else {
				c.fail(key, "option %s: unknown %s %q, known %s: %s", path, word, key.Value, words, known)
			}
			continue
		}
		seen[o.Name] = true
		c.value(value, join(path, o.Name), o)
	}
	for _, o := range fields {
		if o.Required && !seen[o.Name] {
			c.fail(n, "%s: missing required key %s", where(path), o.Name)
		}
	}
}

func where(path string) string {
	if path == "" {
		return "section"
	}
	return "option " + path
}

func findField(fields []registry.Option, name string) (registry.Option, bool) {
	for _, o := range fields {
		if o.Name == name {
			return o, true
		}
	}
	return registry.Option{}, false
}

func join(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// value checks one value against its description.
func (c *checker) value(n *yaml.Node, path string, o registry.Option) {
	if len(o.OneOf) > 0 {
		for _, alt := range o.OneOf {
			if shapeFits(alt.Kind, n) {
				c.value(n, path, alt)
				return
			}
		}
		kinds := make([]string, 0, len(o.OneOf))
		for _, alt := range o.OneOf {
			kinds = append(kinds, article(alt.Kind))
		}
		c.fail(n, "option %s must be %s, got %s", path, strings.Join(kinds, " or "), describe(n))
		return
	}
	if !shapeFits(o.Kind, n) {
		hint := ""
		if o.Kind == registry.KindString && n.Kind == yaml.ScalarNode && n.ShortTag() != "!!null" {
			hint = fmt.Sprintf(": quote it, \"%s\"", n.Value)
		}
		c.fail(n, "option %s must be %s, got %s%s", path, article(o.Kind), describe(n), hint)
		return
	}
	switch o.Kind {
	case registry.KindString:
		if len(o.Enum) > 0 && !slices.Contains(o.Enum, n.Value) {
			c.fail(n, "option %s must be one of %s, got %q", path, strings.Join(o.Enum, ", "), n.Value)
		}
	case registry.KindList:
		for i, item := range n.Content {
			c.value(resolve(item), fmt.Sprintf("%s[%d]", path, i), *o.Items)
		}
	case registry.KindObject:
		c.mapping(n, path, o.Fields, "key", "keys")
	case registry.KindMap:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			if key.ShortTag() == "!!merge" {
				c.fail(key, "option %s: merge keys (<<) are not supported, write the keys out", path)
				continue
			}
			c.value(resolve(n.Content[i+1]), path+"."+key.Value, *o.Values)
		}
	}
}

// shapeFits reports whether the node has the shape of the kind; scalars by their
// resolved tag, so 5 is an int and "5" a string.
func shapeFits(k registry.Kind, n *yaml.Node) bool {
	switch k {
	case registry.KindString:
		return n.Kind == yaml.ScalarNode && n.ShortTag() == "!!str"
	case registry.KindBool:
		return n.Kind == yaml.ScalarNode && n.ShortTag() == "!!bool"
	case registry.KindInt:
		return n.Kind == yaml.ScalarNode && n.ShortTag() == "!!int"
	case registry.KindList:
		return n.Kind == yaml.SequenceNode
	case registry.KindObject, registry.KindMap:
		return n.Kind == yaml.MappingNode
	}
	return false
}

func article(k registry.Kind) string {
	switch k {
	case registry.KindString:
		return "a string"
	case registry.KindBool:
		return "true or false"
	case registry.KindInt:
		return "an integer"
	case registry.KindList:
		return "a list"
	case registry.KindObject:
		return "an object"
	case registry.KindMap:
		return "a map"
	}
	return k.String()
}

// describe names what the file actually holds.
func describe(n *yaml.Node) string {
	switch n.Kind {
	case yaml.SequenceNode:
		return "a list"
	case yaml.MappingNode:
		return "an object"
	}
	switch n.ShortTag() {
	case "!!str":
		return fmt.Sprintf("a string %q", n.Value)
	case "!!int":
		return "an integer " + n.Value
	case "!!float":
		return "a number " + n.Value
	case "!!bool":
		return n.Value
	case "!!null":
		return "nothing: remove the key to use the default"
	}
	return n.ShortTag() + " " + n.Value
}

// DecodeOptions reads the section of a module into a typed struct with yaml tags, the
// way generators read their options. The file is already validated against the
// registry; decoding is strict as well, so a struct that lags behind the registry fails
// in tests instead of dropping an option. A module without a section leaves out as is.
func (f *File) DecodeOptions(module string, out any) error {
	section, ok := f.Modules[module]
	if !ok || len(section) == 0 {
		return nil
	}
	raw, err := yaml.Marshal(section)
	if err != nil {
		return fmt.Errorf("module %s options: %w", module, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("module %s options: %w", module, err)
	}
	return nil
}

// StringOption returns a string option of a module, or def when the module is off, the
// option is not set or is empty.
func (f *File) StringOption(module, option, def string) string {
	if v, ok := f.Modules[module][option].(string); ok && v != "" {
		return v
	}
	return def
}

// optionSchema is the JSON schema fragment of an option, built from the same
// description the validator uses.
func optionSchema(o registry.Option) map[string]any {
	s := map[string]any{}
	if o.Description != "" {
		s["description"] = o.Description
	}
	if o.Default != nil {
		s["default"] = o.Default
	}
	if len(o.OneOf) > 0 {
		alts := make([]any, 0, len(o.OneOf))
		for _, alt := range o.OneOf {
			alts = append(alts, optionSchema(alt))
		}
		s["oneOf"] = alts
		return s
	}
	switch o.Kind {
	case registry.KindString:
		s["type"] = "string"
		if len(o.Enum) > 0 {
			s["enum"] = o.Enum
		}
	case registry.KindBool:
		s["type"] = "boolean"
	case registry.KindInt:
		s["type"] = "integer"
	case registry.KindList:
		s["type"] = "array"
		s["items"] = optionSchema(*o.Items)
	case registry.KindObject:
		s["type"] = "object"
		s["properties"] = propertiesSchema(o.Fields)
		s["additionalProperties"] = false
		if req := required(o.Fields); len(req) > 0 {
			s["required"] = req
		}
	case registry.KindMap:
		s["type"] = "object"
		s["additionalProperties"] = optionSchema(*o.Values)
	}
	return s
}

func propertiesSchema(opts []registry.Option) map[string]any {
	props := make(map[string]any, len(opts))
	for _, o := range opts {
		props[o.Name] = optionSchema(o)
	}
	return props
}

func required(opts []registry.Option) []string {
	var out []string
	for _, o := range opts {
		if o.Required {
			out = append(out, o.Name)
		}
	}
	return out
}
