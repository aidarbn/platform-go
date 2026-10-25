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
	for _, p := range pairs(root) {
		if p.key.Value != "modules" {
			continue
		}
		for _, m := range pairs(resolve(p.value)) {
			out[m.key.Value] = m.value
		}
	}
	return out
}

// pair is a key of a mapping and its value, the value possibly an alias.
type pair struct{ key, value *yaml.Node }

// pairs lists the keys of a mapping the way the decoder sees them: merge keys (<<) are
// expanded, a key written in the mapping wins over a merged one, and of several merged
// mappings the earlier wins. Anything but a mapping has no pairs.
func pairs(n *yaml.Node) []pair {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	var out, merges []pair
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		p := pair{n.Content[i], n.Content[i+1]}
		if isMerge(p.key) {
			merges = append(merges, p)
			continue
		}
		seen[p.key.Value] = true
		out = append(out, p)
	}
	for _, m := range merges {
		sources := []*yaml.Node{resolve(m.value)}
		if sources[0].Kind == yaml.SequenceNode {
			sources = sources[0].Content
		}
		for _, src := range sources {
			for _, p := range pairs(resolve(src)) {
				if !seen[p.key.Value] {
					seen[p.key.Value] = true
					out = append(out, p)
				}
			}
		}
	}
	return out
}

// isMerge matches the merge key as yaml.v3 does.
func isMerge(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Value == "<<" && (n.Tag == "" || n.Tag == "!" || n.ShortTag() == "!!merge")
}

// resolve follows an alias to the node it names.
func resolve(n *yaml.Node) *yaml.Node {
	for n != nil && n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

// checkOptions rejects an option the module does not have and a value of the wrong
// type: a typo in platformgo.yaml must fail rather than be ignored. An empty section
// (null) is checked as {}, so a required option is required there too.
func checkOptions(m registry.Module, section *yaml.Node) []error {
	c := checker{module: m.Name}
	at := section
	section = resolve(section)
	if section == nil || section.Kind != yaml.MappingNode {
		if at == nil {
			at = &yaml.Node{}
		}
		section = &yaml.Node{Kind: yaml.MappingNode, Line: at.Line}
	}
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
	for _, p := range pairs(n) {
		key, value := p.key, p.value
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
			if path == "" {
				c.fail(n, "missing required option %s", o.Name)
			} else {
				c.fail(n, "option %s: missing required key %s", path, o.Name)
			}
		}
	}
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

// value checks one value against its description. at is the node as written: for an
// alias, errors name the line of the alias, not of the anchor.
func (c *checker) value(at *yaml.Node, path string, o registry.Option) {
	n := resolve(at)
	if len(o.OneOf) > 0 {
		for _, alt := range o.OneOf {
			if shapeFits(alt.Kind, n) {
				c.value(at, path, alt)
				return
			}
		}
		kinds := make([]string, 0, len(o.OneOf))
		for _, alt := range o.OneOf {
			kinds = append(kinds, article(alt.Kind))
		}
		c.fail(at, "option %s must be %s, got %s", path, strings.Join(kinds, " or "), describe(n))
		return
	}
	if !shapeFits(o.Kind, n) {
		hint := ""
		if o.Kind == registry.KindString && n.Kind == yaml.ScalarNode && n.ShortTag() != "!!null" {
			hint = fmt.Sprintf(": quote it, \"%s\"", n.Value)
		}
		c.fail(at, "option %s must be %s, got %s%s", path, article(o.Kind), describe(n), hint)
		return
	}
	switch o.Kind {
	case registry.KindString:
		if len(o.Enum) > 0 && !slices.Contains(o.Enum, n.Value) {
			c.fail(at, "option %s must be one of %s, got %q", path, strings.Join(o.Enum, ", "), n.Value)
		}
	case registry.KindList:
		for i, item := range n.Content {
			c.value(item, fmt.Sprintf("%s[%d]", path, i), *o.Items)
		}
	case registry.KindObject:
		c.mapping(n, path, o.Fields, "key", "keys")
	case registry.KindMap:
		for _, p := range pairs(n) {
			c.value(p.value, path+"."+p.key.Value, *o.Values)
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
	case "!!timestamp":
		return "a date " + n.Value
	}
	return n.ShortTag() + " " + n.Value
}

// DecodeOptions reads the section of a module into a typed struct with yaml tags, the
// way generators read their options. Defaults declared in the registry fill what the
// file leaves out, and an empty string counts as left out, as in StringOption. Decoding
// is strict: a key the struct has no field for is an error. A disabled module leaves
// out as it is.
func (f *File) DecodeOptions(module string, out any) error {
	section, ok := f.Modules[module]
	if !ok {
		return nil
	}
	m, known := registry.Get(module)
	if !known {
		return fmt.Errorf("module %s options: unknown module", module)
	}
	raw, err := yaml.Marshal(withDefaults(m.Options, section))
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

// withDefaults returns a copy of values with the defaults of opts filled in, also inside
// objects, list elements and map values.
func withDefaults(opts []registry.Option, values map[string]any) map[string]any {
	out := make(map[string]any, len(opts))
	for k, v := range values {
		out[k] = v
	}
	for _, o := range opts {
		v, set := out[o.Name]
		if s, isString := v.(string); o.Default != nil && (!set || isString && s == "") {
			v, set = o.Default, true
		}
		if set {
			out[o.Name] = fillDefaults(o, v)
			continue
		}
		// An object left out still has the defaults of its fields: a generator reads
		// sqlc.dir whether or not the file mentions sqlc.
		if o.Kind == registry.KindObject && len(o.OneOf) == 0 {
			if inner := withDefaults(o.Fields, nil); len(inner) > 0 {
				out[o.Name] = inner
			}
		}
	}
	return out
}

func fillDefaults(o registry.Option, v any) any {
	if len(o.OneOf) > 0 {
		for _, alt := range o.OneOf {
			if alt.Kind == registry.KindObject {
				return fillDefaults(alt, v)
			}
		}
		return v
	}
	switch o.Kind {
	case registry.KindObject:
		if m, ok := v.(map[string]any); ok {
			return withDefaults(o.Fields, m)
		}
	case registry.KindList:
		if items, ok := v.([]any); ok {
			out := make([]any, len(items))
			for i, item := range items {
				out[i] = fillDefaults(*o.Items, item)
			}
			return out
		}
	case registry.KindMap:
		if m, ok := v.(map[string]any); ok {
			out := make(map[string]any, len(m))
			for k, item := range m {
				out[k] = fillDefaults(*o.Values, item)
			}
			return out
		}
	}
	return v
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
