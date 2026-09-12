// Package settingsx holds the business settings of a project: the schema, the current
// values and typed access to them.
//
// Business settings are what an administrator changes without a developer and without a
// deploy: schedules, limits, timeouts, flags. Technical parameters of modules live in
// platformgo.yaml and environment variables instead.
package settingsx

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Kind is the type of a setting. It decides how the raw value is parsed, which
// constraints apply and which control the admin UI shows.
type Kind string

const (
	KindBool     Kind = "bool"
	KindInt      Kind = "int"
	KindDuration Kind = "duration"
	KindString   Kind = "string"
	KindCron     Kind = "cron"
)

// Kinds returns every supported kind.
func Kinds() []Kind { return []Kind{KindBool, KindInt, KindDuration, KindString, KindCron} }

func (k Kind) valid() bool { return slices.Contains(Kinds(), k) }

// Definition describes one setting. Values are kept as text: that is what the database
// stores and what the admin UI edits, while typed access parses on read.
type Definition struct {
	Key     string   // full key, "orders.create.max_attempts"
	Group   string   // group, "orders.create"
	Name    string   // name inside the group, "max_attempts"
	Kind    Kind     //
	Default string   // value used until the database says otherwise
	Min     string   // lower bound for int and duration; empty means unbounded
	Max     string   // upper bound for int and duration
	Options []string // allowed values for string
	Title   string   // human readable name for the admin UI
}

// Group is a set of settings shown together in the admin UI.
type Group struct {
	Name        string
	Definitions []Definition
}

// Schema is the settings of a project. It is generated from settings.yaml, so code
// never refers to a key the schema does not describe.
type Schema struct {
	defs  []Definition
	byKey map[string]Definition
}

// NewSchema validates the definitions and builds the schema.
func NewSchema(defs ...Definition) (Schema, error) {
	s := Schema{
		defs:  make([]Definition, 0, len(defs)),
		byKey: make(map[string]Definition, len(defs)),
	}

	var errs []error
	for _, def := range defs {
		if err := validateDefinition(def); err != nil {
			errs = append(errs, err)
			continue
		}
		if _, dup := s.byKey[def.Key]; dup {
			errs = append(errs, fmt.Errorf("%s: duplicate key", def.Key))
			continue
		}
		s.byKey[def.Key] = def
		s.defs = append(s.defs, def)
	}
	if len(errs) > 0 {
		return Schema{}, errors.Join(errs...)
	}

	slices.SortFunc(s.defs, func(a, b Definition) int { return strings.Compare(a.Key, b.Key) })
	return s, nil
}

// MustSchema is NewSchema for generated code: a broken schema is a generation bug and
// must surface on the first run, not in production.
func MustSchema(defs ...Definition) Schema {
	s, err := NewSchema(defs...)
	if err != nil {
		panic("settingsx: bad schema: " + err.Error())
	}
	return s
}

func validateDefinition(def Definition) error {
	if def.Key == "" {
		return errors.New("a setting without a key")
	}
	if def.Group == "" || def.Name == "" {
		return fmt.Errorf("%s: group and name are required", def.Key)
	}
	if def.Key != def.Group+"."+def.Name {
		return fmt.Errorf("%s: key must be %q", def.Key, def.Group+"."+def.Name)
	}
	if !def.Kind.valid() {
		return fmt.Errorf("%s: unknown type %q, supported: %s", def.Key, def.Kind, joinKinds())
	}
	if len(def.Options) > 0 && def.Kind != KindString {
		return fmt.Errorf("%s: options are only allowed for type string", def.Key)
	}
	if (def.Min != "" || def.Max != "") && def.Kind != KindInt && def.Kind != KindDuration {
		return fmt.Errorf("%s: min and max are only allowed for types int and duration", def.Key)
	}
	for _, bound := range []struct {
		name string
		raw  string
	}{{"min", def.Min}, {"max", def.Max}} {
		if bound.raw == "" {
			continue
		}
		if err := parseScalar(def.Kind, bound.raw); err != nil {
			return fmt.Errorf("%s: %s: %w", def.Key, bound.name, err)
		}
	}
	if def.Default == "" && def.Kind != KindString {
		return fmt.Errorf("%s: a default is required", def.Key)
	}
	if err := validateValue(def, def.Default); err != nil {
		return fmt.Errorf("%s: default: %w", def.Key, err)
	}
	return nil
}

func joinKinds() string {
	out := make([]string, 0, len(Kinds()))
	for _, k := range Kinds() {
		out = append(out, string(k))
	}
	return strings.Join(out, ", ")
}

// Definitions returns every setting ordered by key.
func (s Schema) Definitions() []Definition { return slices.Clone(s.defs) }

// Definition returns one setting by key.
func (s Schema) Definition(key string) (Definition, bool) {
	def, ok := s.byKey[key]
	return def, ok
}

// Groups returns the settings grouped, in group name order.
func (s Schema) Groups() []Group {
	var groups []Group
	for _, def := range s.defs {
		if n := len(groups); n > 0 && groups[n-1].Name == def.Group {
			groups[n-1].Definitions = append(groups[n-1].Definitions, def)
			continue
		}
		groups = append(groups, Group{Name: def.Group, Definitions: []Definition{def}})
	}
	slices.SortFunc(groups, func(a, b Group) int { return strings.Compare(a.Name, b.Name) })
	return groups
}

// Validate checks a raw value against the setting, the way the admin UI and Set do.
func (s Schema) Validate(key, raw string) error {
	def, ok := s.byKey[key]
	if !ok {
		return fmt.Errorf("unknown setting %q", key)
	}
	if err := validateValue(def, raw); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	return nil
}

func validateValue(def Definition, raw string) error {
	switch def.Kind {
	case KindBool, KindInt, KindDuration:
		if err := parseScalar(def.Kind, raw); err != nil {
			return err
		}
		return checkBounds(def, raw)
	case KindString:
		if len(def.Options) > 0 && !slices.Contains(def.Options, raw) {
			return fmt.Errorf("expected one of %s, got %q", strings.Join(def.Options, ", "), raw)
		}
		return nil
	case KindCron:
		return validateCron(raw)
	default:
		return fmt.Errorf("unknown type %q", def.Kind)
	}
}

// parseScalar checks that the text matches the kind. It is used both for values and
// for the min and max bounds, so a bad bound in the schema is caught as well.
func parseScalar(kind Kind, raw string) error {
	switch kind {
	case KindBool:
		if _, err := strconv.ParseBool(raw); err != nil {
			return fmt.Errorf("expected true or false, got %q", raw)
		}
	case KindInt:
		if _, err := strconv.Atoi(raw); err != nil {
			return fmt.Errorf("expected an integer, got %q", raw)
		}
	case KindDuration:
		if _, err := time.ParseDuration(raw); err != nil {
			return fmt.Errorf("expected a duration such as 30s, got %q", raw)
		}
	}
	return nil
}

func checkBounds(def Definition, raw string) error {
	switch def.Kind {
	case KindInt:
		n, _ := strconv.Atoi(raw)
		if def.Min != "" {
			if low, _ := strconv.Atoi(def.Min); n < low {
				return fmt.Errorf("%d is below the minimum %d", n, low)
			}
		}
		if def.Max != "" {
			if high, _ := strconv.Atoi(def.Max); n > high {
				return fmt.Errorf("%d is above the maximum %d", n, high)
			}
		}
	case KindDuration:
		d, _ := time.ParseDuration(raw)
		if def.Min != "" {
			if low, _ := time.ParseDuration(def.Min); d < low {
				return fmt.Errorf("%s is below the minimum %s", d, low)
			}
		}
		if def.Max != "" {
			if high, _ := time.ParseDuration(def.Max); d > high {
				return fmt.Errorf("%s is above the maximum %s", d, high)
			}
		}
	}
	return nil
}

// cronMacros are the shorthand schedules accepted instead of five fields.
var cronMacros = []string{"@yearly", "@annually", "@monthly", "@weekly", "@daily", "@midnight", "@hourly"}

// validateCron is a sanity check, not a full parser: it catches a typo in the admin UI
// before the value reaches the scheduler, which parses it for real.
func validateCron(raw string) error {
	expr := strings.TrimSpace(raw)
	if expr == "" {
		return errors.New("an empty schedule")
	}
	if strings.HasPrefix(expr, "@") {
		if slices.Contains(cronMacros, strings.ToLower(expr)) {
			return nil
		}
		if rest, ok := cutPrefixFold(expr, "@every "); ok {
			d, err := time.ParseDuration(strings.TrimSpace(rest))
			if err != nil {
				return fmt.Errorf("@every wants a duration such as 1h, got %q", strings.TrimSpace(rest))
			}
			if d <= 0 {
				return fmt.Errorf("@every wants a positive interval, got %s", d)
			}
			return nil
		}
		return fmt.Errorf("unknown shorthand %q, supported: %s, @every <duration>", expr, strings.Join(cronMacros, ", "))
	}

	fields := strings.Fields(expr)
	if len(fields) != 5 && len(fields) != 6 {
		return fmt.Errorf("expected 5 or 6 fields, got %d in %q", len(fields), expr)
	}
	for i, f := range fields {
		if !validCronField(f) {
			return fmt.Errorf("field %d is not a schedule: %q", i+1, f)
		}
	}
	return nil
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
}

func validCronField(f string) bool {
	if f == "" {
		return false
	}
	for _, r := range f {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r == '*' || r == '/' || r == ',' || r == '-' || r == '?' || r == '#' || r == 'L' || r == 'W':
		default:
			return false
		}
	}
	return true
}
