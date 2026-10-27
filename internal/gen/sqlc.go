package gen

import (
	"errors"
	"fmt"
	"go/token"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"

	"github.com/aidarbn/platform-go/internal/lock"
	"github.com/aidarbn/platform-go/internal/spec"
)

// SqlcPath is the sqlc configuration of a project that keeps the defaults of the sqlc
// option of the postgres module.
const SqlcPath = "db/sqlc.yaml"

// SqlcSettings is the sqlc option of the postgres module: where the configuration, the
// queries and the generated code live, and the type mapping on top of the platform's.
// Paths use forward slashes; dir and out are relative to the project root, queries to
// dir.
type SqlcSettings struct {
	Dir           string            `yaml:"dir"`
	Queries       string            `yaml:"queries"`
	Out           string            `yaml:"out"`
	Package       string            `yaml:"package"`
	EmitInterface bool              `yaml:"emit_interface"`
	Overrides     []SqlcOverride    `yaml:"overrides"`
	Renames       map[string]string `yaml:"renames"`
}

// SqlcOverride maps a database type or a column to a Go type, as an override of sqlc.
type SqlcOverride struct {
	DBType   string     `yaml:"db_type"`
	Column   string     `yaml:"column"`
	Nullable bool       `yaml:"nullable"`
	GoType   SqlcGoType `yaml:"go_type"`
}

// SqlcGoType is the go_type of an override: a type name ("int64") or an object.
type SqlcGoType struct {
	Import  string `yaml:"import"`
	Package string `yaml:"package"`
	Type    string `yaml:"type"`
	Pointer bool   `yaml:"pointer"`
	Slice   bool   `yaml:"slice"`

	short bool // written as a plain type name
}

// UnmarshalYAML accepts both shapes sqlc accepts: go_type: int64 and
// go_type: {type: int64, pointer: true}.
func (g *SqlcGoType) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*g = SqlcGoType{Type: n.Value, short: true}
		return nil
	}
	type plain SqlcGoType
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*g = SqlcGoType(p)
	return nil
}

// ConfigPath is where the sqlc configuration is generated.
func (s SqlcSettings) ConfigPath() string { return path.Join(s.Dir, "sqlc.yaml") }

// QueriesDir is the folder of the static queries, relative to the project root.
func (s SqlcSettings) QueriesDir() string { return path.Join(s.Dir, s.Queries) }

// Sqlc reads the sqlc option of the postgres module, with the defaults for what the file
// leaves out.
func Sqlc(f *spec.File) (SqlcSettings, error) {
	var opts struct {
		Sqlc SqlcSettings `yaml:"sqlc"`
	}
	if err := f.DecodeOptions("postgres", &opts); err != nil {
		return SqlcSettings{}, err
	}
	s := opts.Sqlc
	s.Dir, s.Queries, s.Out = clean(s.Dir), clean(s.Queries), clean(s.Out)
	if err := s.check(); err != nil {
		return SqlcSettings{}, fmt.Errorf("module postgres: option sqlc: %w", err)
	}
	return s, nil
}

func clean(p string) string { return path.Clean(strings.TrimSpace(p)) }

// jetOut is where jet writes the dynamic query builder.
const jetOut = "internal/db/jetgen"

// within reports whether p is base or a folder inside it.
func within(p, base string) bool {
	return base == "." || p == base || strings.HasPrefix(p, base+"/")
}

func (s SqlcSettings) check() error {
	var errs []error
	for _, p := range []struct{ name, value, raw string }{
		{"dir", s.Dir, s.Dir}, {"queries", s.QueriesDir(), s.Queries}, {"out", s.Out, s.Out},
	} {
		if !lock.SafePath(p.value) || path.IsAbs(p.raw) || strings.ContainsFunc(p.raw, unicode.IsControl) {
			errs = append(errs, fmt.Errorf("%s %q must be a folder inside the project", p.name, p.raw))
		}
	}
	// sqlc removes its old output from out before generating, folders that become
	// empty too: out must not hold or sit in another folder of the query generation.
	for _, other := range []struct{ name, value string }{
		{"dir", s.Dir}, {"queries", s.QueriesDir()}, {"migrations", MigrationsDir}, {"jet output", jetOut},
	} {
		if within(other.value, s.Out) || within(s.Out, other.value) && other.name != "dir" {
			errs = append(errs, fmt.Errorf("out %q overlaps the %s folder %s", s.Out, other.name, other.value))
		}
	}
	if !token.IsIdentifier(s.Package) {
		errs = append(errs, fmt.Errorf("package %q is not a Go package name", s.Package))
	}
	for i, o := range s.Overrides {
		switch {
		case o.DBType == "" && o.Column == "":
			errs = append(errs, fmt.Errorf("overrides[%d]: set db_type or column", i))
		case o.DBType != "" && o.Column != "":
			errs = append(errs, fmt.Errorf("overrides[%d]: db_type and column exclude each other", i))
		}
		if o.GoType.Type == "" {
			errs = append(errs, fmt.Errorf("overrides[%d]: go_type needs a type", i))
		}
	}
	return errors.Join(errs...)
}

// platformOverrides is the type mapping of the platform. The overrides of the project
// come before it: sqlc takes the first override that matches.
const platformOverrides = `          # Money and percentages are exact: numeric is never a float.
          - db_type: pg_catalog.numeric
            go_type: { import: github.com/shopspring/decimal, type: Decimal }
          - db_type: pg_catalog.numeric
            nullable: true
            go_type: { import: github.com/shopspring/decimal, type: NullDecimal }
          - db_type: uuid
            go_type: { import: github.com/google/uuid, type: UUID }
          - db_type: uuid
            nullable: true
            go_type: { import: github.com/google/uuid, type: UUID, pointer: true }
          - db_type: jsonb
            go_type: { import: encoding/json, type: RawMessage }
          - db_type: jsonb
            nullable: true
            go_type: { import: encoding/json, type: RawMessage }
          - db_type: timestamptz
            go_type: { import: time, type: Time }
          - db_type: timestamptz
            nullable: true
            go_type: { import: time, type: Time, pointer: true }
          - db_type: date
            go_type: { import: time, type: Time }
          - db_type: date
            nullable: true
            go_type: { import: time, type: Time, pointer: true }
`

// sqlcConfig renders sqlc.yaml. With the defaults it is the file every project had
// before the settings existed, byte for byte.
func sqlcConfig(s SqlcSettings) []byte {
	var b strings.Builder
	b.WriteString("# Generated by platformgo from platformgo.yaml. Do not edit.\n")
	b.WriteString("# Static queries: put them into " + s.QueriesDir() + "/*.sql and run make generate.\n")
	b.WriteString("version: \"2\"\nsql:\n  - engine: postgresql\n")
	b.WriteString("    schema: " + sqlcString(relTo(s.Dir, MigrationsDir)) + "\n")
	b.WriteString("    queries: " + sqlcString(s.Queries) + "\n")
	b.WriteString("    gen:\n      go:\n")
	b.WriteString("        package: " + sqlcString(s.Package) + "\n")
	b.WriteString("        out: " + sqlcString(relTo(s.Dir, s.Out)) + "\n")
	b.WriteString("        sql_package: pgx/v5\n")
	b.WriteString("        emit_pointers_for_null_types: true\n")
	b.WriteString("        emit_interface: " + strconv.FormatBool(s.EmitInterface) + "\n")
	if len(s.Renames) > 0 {
		b.WriteString("        rename:\n")
		for _, k := range slices.Sorted(maps.Keys(s.Renames)) {
			b.WriteString("          " + sqlcString(k) + ": " + sqlcString(s.Renames[k]) + "\n")
		}
	}
	b.WriteString("        overrides:\n")
	if len(s.Overrides) > 0 {
		b.WriteString("          # From platformgo.yaml; they come first, and sqlc takes the first match.\n")
		for _, o := range s.Overrides {
			writeOverride(&b, o)
		}
	}
	b.WriteString(platformOverrides)
	return []byte(b.String())
}

func writeOverride(b *strings.Builder, o SqlcOverride) {
	if o.DBType != "" {
		b.WriteString("          - db_type: " + sqlcString(o.DBType) + "\n")
	} else {
		b.WriteString("          - column: " + sqlcString(o.Column) + "\n")
	}
	if o.Nullable {
		b.WriteString("            nullable: true\n")
	}
	g := o.GoType
	if g.short {
		b.WriteString("            go_type: " + sqlcString(g.Type) + "\n")
		return
	}
	var parts []string
	if g.Import != "" {
		parts = append(parts, "import: "+sqlcString(g.Import))
	}
	if g.Package != "" {
		parts = append(parts, "package: "+sqlcString(g.Package))
	}
	parts = append(parts, "type: "+sqlcString(g.Type))
	if g.Pointer {
		parts = append(parts, "pointer: true")
	}
	if g.Slice {
		parts = append(parts, "slice: true")
	}
	b.WriteString("            go_type: { " + strings.Join(parts, ", ") + " }\n")
}

// relTo is target relative to dir; both are clean slash paths inside the project.
func relTo(dir, target string) string {
	up := 0
	for dir != "." && target != dir && !strings.HasPrefix(target, dir+"/") {
		dir = path.Dir(dir)
		up++
	}
	rest := target
	if dir != "." {
		rest = strings.TrimPrefix(strings.TrimPrefix(target, dir), "/")
	}
	parts := slices.Repeat([]string{".."}, up)
	if rest != "" {
		parts = append(parts, rest)
	}
	if len(parts) == 0 {
		return "."
	}
	return strings.Join(parts, "/")
}

var plainYAML = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_./-]*$`)

// sqlcString writes a value plain when YAML reads it back as the same string, and
// quoted otherwise.
func sqlcString(s string) string {
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "on", "off", "y", "n", "null", "~", ".inf", ".nan":
		return strconv.Quote(s)
	}
	// YAML reads 1_000, 0x1F and 0o17 as numbers: anything that starts like one is quoted.
	if s == "" || s[0] >= '0' && s[0] <= '9' || s[0] == '.' && len(s) > 1 && s[1] >= '0' && s[1] <= '9' {
		return strconv.Quote(s)
	}
	if plainYAML.MatchString(s) {
		return s
	}
	return strconv.Quote(s)
}
