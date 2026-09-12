package gen

import (
	"fmt"
	"strings"
	"text/template"

	"github.com/aidarbn/platform-go/internal/settingsdef"
	"github.com/aidarbn/platform-go/internal/spec"
	"github.com/aidarbn/platform-go/kit/settingsx"
)

// SettingsPath is the generated typed access to the business settings. It lives in the
// project so that domain code reads a setting as a method call rather than a string key.
const SettingsPath = "internal/settings/settings.gen.go"

// SettingsModule is the module name whose schema drives the generation.
const SettingsModule = "settings"

// SettingsEnabled reports whether the project enables the settings module.
func SettingsEnabled(f *spec.File) bool {
	_, ok := f.Modules[SettingsModule]
	return ok
}

// SettingsSchemaPath is the schema file of the project: settings.yaml unless the
// module section names another one.
func SettingsSchemaPath(f *spec.File) string {
	if cfg := f.Modules[SettingsModule]; cfg != nil {
		if v, ok := cfg["schema"].(string); ok && v != "" {
			return v
		}
	}
	return settingsdef.FileName
}

var settingsTmpl = template.Must(template.New("settings").Parse(header + `
package settings

import (
{{- range .Imports}}
	{{.}}
{{- end}}
)

// Schema describes the business settings of the project. It is generated from the
// schema file, so code cannot read a setting the schema does not describe.
var Schema = settingsx.MustSchema(
{{- range .Definitions}}
	{{.}},
{{- end}}
)

// Settings is typed access to the business settings.
type Settings struct{ store *settingsx.Store }

// From returns the settings of the project from the container.
func From(app *platform.App) *Settings { return &Settings{store: settingsx.From(app)} }

// New wraps a ready store. Project tests use it together with settingsx.NewTestStore.
func New(store *settingsx.Store) *Settings { return &Settings{store: store} }

// Store returns the underlying store: the admin UI edits values through it.
func (s *Settings) Store() *settingsx.Store { return s.store }
{{- range $group := .Groups}}

// {{.Method}} returns the settings of group {{printf "%q" .Name}}.
func (s *Settings) {{.Method}}() {{.Type}} { return {{.Type}}{store: s.store} }

// {{.Type}} is the group {{printf "%q" .Name}}.
type {{.Type}} struct{ store *settingsx.Store }
{{- range .Settings}}

// {{.Method}} returns {{printf "%q" .Key}}.
func (g {{$group.Type}}) {{.Method}}() {{.GoType}} { return g.store.{{.Getter}}({{printf "%q" .Key}}) }
{{- end}}
{{- end}}
`))

type settingsData struct {
	Imports     []string
	Definitions []string
	Groups      []settingsGroup
}

type settingsGroup struct {
	Name     string
	Method   string
	Type     string
	Settings []settingsField
}

type settingsField struct {
	Key    string
	Method string
	GoType string
	Getter string
}

// SettingsCode generates the typed access from the schema file contents.
func SettingsCode(raw []byte) ([]byte, error) {
	schema, err := settingsdef.Parse(raw)
	if err != nil {
		return nil, err
	}
	data, err := settingsData_(schema)
	if err != nil {
		return nil, err
	}
	return renderSettings(data)
}

func settingsData_(schema settingsx.Schema) (settingsData, error) {
	data := settingsData{
		Imports: []string{
			`"github.com/aidarbn/platform-go/kit/platform"`,
			`"github.com/aidarbn/platform-go/kit/settingsx"`,
		},
	}

	// Duration settings are the only reason to import time, and an unused import does
	// not compile, so the import list follows the schema.
	for _, def := range schema.Definitions() {
		if def.Kind == settingsx.KindDuration {
			data.Imports = append([]string{`"time"`, ""}, data.Imports...)
			break
		}
	}

	for _, def := range schema.Definitions() {
		data.Definitions = append(data.Definitions, definitionLiteral(def))
	}

	types := make(map[string]string, len(schema.Groups()))
	for _, group := range schema.Groups() {
		method := exported(group.Name)
		if other, clash := types[method]; clash {
			return settingsData{}, fmt.Errorf("groups %q and %q give the same name %s: rename one", other, group.Name, method)
		}
		types[method] = group.Name

		g := settingsGroup{Name: group.Name, Method: method, Type: method + "Settings"}
		names := make(map[string]string, len(group.Definitions))
		for _, def := range group.Definitions {
			name := exported(def.Name)
			if other, clash := names[name]; clash {
				return settingsData{}, fmt.Errorf("settings %q and %q of group %q give the same name %s: rename one", other, def.Name, group.Name, name)
			}
			names[name] = def.Name

			g.Settings = append(g.Settings, settingsField{
				Key:    def.Key,
				Method: name,
				GoType: goType(def.Kind),
				Getter: getter(def.Kind),
			})
		}
		data.Groups = append(data.Groups, g)
	}
	return data, nil
}

func definitionLiteral(def settingsx.Definition) string {
	fields := []string{
		fmt.Sprintf("Key: %q", def.Key),
		fmt.Sprintf("Group: %q", def.Group),
		fmt.Sprintf("Name: %q", def.Name),
		"Kind: settingsx." + kindConst(def.Kind),
		fmt.Sprintf("Default: %q", def.Default),
	}
	if def.Min != "" {
		fields = append(fields, fmt.Sprintf("Min: %q", def.Min))
	}
	if def.Max != "" {
		fields = append(fields, fmt.Sprintf("Max: %q", def.Max))
	}
	if len(def.Options) > 0 {
		quoted := make([]string, 0, len(def.Options))
		for _, o := range def.Options {
			quoted = append(quoted, fmt.Sprintf("%q", o))
		}
		fields = append(fields, "Options: []string{"+strings.Join(quoted, ", ")+"}")
	}
	if def.Title != "" {
		fields = append(fields, fmt.Sprintf("Title: %q", def.Title))
	}
	return "settingsx.Definition{" + strings.Join(fields, ", ") + "}"
}

func kindConst(kind settingsx.Kind) string { return "Kind" + exported(string(kind)) }

func goType(kind settingsx.Kind) string {
	switch kind {
	case settingsx.KindBool:
		return "bool"
	case settingsx.KindInt:
		return "int"
	case settingsx.KindDuration:
		return "time.Duration"
	default:
		return "string"
	}
}

func getter(kind settingsx.Kind) string { return exported(string(kind)) }

// exported turns a key part into a Go name: "max_attempts" gives "MaxAttempts",
// "orders.cleanup" gives "OrdersCleanup".
func exported(s string) string {
	var b strings.Builder
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '_' || r == '-' }) {
		runes := []rune(part)
		b.WriteString(strings.ToUpper(string(runes[0])))
		if len(runes) > 1 {
			b.WriteString(string(runes[1:]))
		}
	}
	return b.String()
}

func renderSettings(data settingsData) ([]byte, error) {
	return renderTemplate(settingsTmpl, data)
}
