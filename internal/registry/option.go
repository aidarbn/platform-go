package registry

import (
	"errors"
	"fmt"
	"slices"
)

// Kind is the type of a module option value in platformgo.yaml.
type Kind int

// The zero Kind is a string, so an option described before kinds existed stays a string.
const (
	KindString Kind = iota
	KindBool
	KindInt
	KindList   // a sequence of Items
	KindObject // a mapping with the keys listed in Fields, nothing else
	KindMap    // a mapping with any string keys and values of the Values type
)

// String is the name of the kind as error messages and the docs use it.
func (k Kind) String() string {
	switch k {
	case KindString:
		return "string"
	case KindBool:
		return "bool"
	case KindInt:
		return "int"
	case KindList:
		return "list"
	case KindObject:
		return "object"
	case KindMap:
		return "map"
	}
	return fmt.Sprintf("Kind(%d)", int(k))
}

// Option is a technical setting of a module in platformgo.yaml, or a part of one: an
// element of a list, a field of an object, a value of a map. The same description drives
// validation of platformgo.yaml and the JSON schema, so the two cannot disagree.
type Option struct {
	Name        string // key in the section; empty for list elements, map values and alternatives
	Description string
	Kind        Kind
	Default     any // documented in the schema; the generator applies it, the file may omit the key

	Enum     []string // allowed values of a string
	Required bool     // a field of an object that must be present

	Items  *Option  // element of a list
	Fields []Option // keys of an object
	Values *Option  // value of a map

	// OneOf lists alternative shapes of the value, for example a Go type that is either
	// "int64" or {type: int64, pointer: true}. The value must match one of them; the
	// other fields of the option except Name, Description and Default are not used.
	OneOf []Option
}

// Option returns an option of the module by name.
func (m Module) Option(name string) (Option, bool) { return findOption(m.Options, name) }

// Field returns a field of an object option by name.
func (o Option) Field(name string) (Option, bool) { return findOption(o.Fields, name) }

func findOption(opts []Option, name string) (Option, bool) {
	for _, o := range opts {
		if o.Name == name {
			return o, true
		}
	}
	return Option{}, false
}

// OptionNames lists the names of options in their declared order.
func OptionNames(opts []Option) []string {
	out := make([]string, 0, len(opts))
	for _, o := range opts {
		out = append(out, o.Name)
	}
	return out
}

// CheckOptions reports a malformed description of module options: a list without the
// element type, an object without fields, a duplicate name, an enum on a non-string.
// Registry tests run it on every module, so a mistake fails there and not in a project.
func CheckOptions(m Module) error {
	var errs []error
	checkOptionList(&errs, m.Name, m.Options)
	return errors.Join(errs...)
}

func checkOptionList(errs *[]error, path string, opts []Option) {
	seen := map[string]bool{}
	for _, o := range opts {
		p := path + "." + o.Name
		if o.Name == "" {
			*errs = append(*errs, fmt.Errorf("%s: an option without a name", path))
		} else if seen[o.Name] {
			*errs = append(*errs, fmt.Errorf("%s: duplicate option", p))
		}
		seen[o.Name] = true
		if o.Description == "" {
			*errs = append(*errs, fmt.Errorf("%s: no description", p))
		}
		checkOption(errs, p, o)
	}
}

func checkOption(errs *[]error, path string, o Option) {
	fail := func(format string, args ...any) {
		*errs = append(*errs, fmt.Errorf(path+": "+format, args...))
	}
	if len(o.OneOf) > 0 {
		if len(o.OneOf) < 2 {
			fail("one_of needs at least two alternatives")
		}
		if o.Kind != KindString || o.Items != nil || o.Fields != nil || o.Values != nil || o.Enum != nil {
			fail("one_of excludes kind, items, fields, values and enum")
		}
		kinds := map[Kind]bool{}
		fits := o.Default == nil
		for i, alt := range o.OneOf {
			fits = fits || defaultFits(alt.Kind, o.Default)
			if alt.Name != "" || alt.Required || len(alt.OneOf) > 0 {
				fail("alternative %d: no name, required or nested one_of", i)
			}
			if kinds[alt.Kind] {
				fail("alternative %d: two alternatives of kind %s cannot be told apart", i, alt.Kind)
			}
			kinds[alt.Kind] = true
			checkOption(errs, fmt.Sprintf("%s|%s", path, alt.Kind), alt)
		}
		if kinds[KindObject] && kinds[KindMap] {
			fail("an object and a map cannot be told apart")
		}
		if !fits {
			fail("default %#v fits none of the alternatives", o.Default)
		}
		return
	}
	if len(o.Enum) > 0 {
		if o.Kind != KindString {
			fail("enum is allowed only on a string, not a %s", o.Kind)
		}
		if d, ok := o.Default.(string); ok && !slices.Contains(o.Enum, d) {
			fail("default %q is not one of the enum", d)
		}
	}
	if o.Default != nil && !defaultFits(o.Kind, o.Default) {
		fail("default %#v is not a %s", o.Default, o.Kind)
	}
	if (o.Items != nil) != (o.Kind == KindList) {
		fail("items go with a list and only with a list")
	}
	if (len(o.Fields) > 0) != (o.Kind == KindObject) {
		fail("fields go with an object and only with an object")
	}
	if (o.Values != nil) != (o.Kind == KindMap) {
		fail("values go with a map and only with a map")
	}
	if o.Kind > KindMap || o.Kind < KindString {
		fail("unknown kind %d", int(o.Kind))
	}
	if o.Items != nil {
		checkElement(errs, path+"[]", *o.Items)
	}
	if o.Values != nil {
		checkElement(errs, path+"{}", *o.Values)
	}
	if len(o.Fields) > 0 {
		checkOptionList(errs, path, o.Fields)
	}
}

func checkElement(errs *[]error, path string, o Option) {
	if o.Name != "" || o.Required {
		*errs = append(*errs, fmt.Errorf("%s: an element has no name and is not required", path))
	}
	checkOption(errs, path, o)
}

func defaultFits(k Kind, v any) bool {
	switch k {
	case KindString:
		_, ok := v.(string)
		return ok
	case KindBool:
		_, ok := v.(bool)
		return ok
	case KindInt:
		_, ok := v.(int)
		return ok
	case KindList:
		_, ok := v.([]any)
		return ok
	case KindObject, KindMap:
		_, ok := v.(map[string]any)
		return ok
	}
	return false
}
