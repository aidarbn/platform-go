package gen

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"

	"github.com/aidarbn/platform-go/internal/spec"
)

// EnumsPath is the generated catalog of the enums of the project.
const EnumsPath = "internal/enums/enums.gen.go"

// DefaultDomainDir is where enums are declared unless the enums module says otherwise.
const DefaultDomainDir = "internal/domain"

// enumMarker is taply's marker above a const block:
//
//	// go-enum: order.status "Order status" entity="Orders" order=10
var enumMarker = regexp.MustCompile(`^\s*//\s*go-enum:\s*([a-z_]+)\.([a-z_]+)\s+"([^"]*)"\s+entity="([^"]*)"\s+order=(-?\d+)\s*$`)

type enumValue struct {
	Const       string
	Description string
}

type enumDef struct {
	Name, Description string
	Typed             bool
	Values            []enumValue
}

type enumEntity struct {
	Entity, Description string
	Order               int
	Enums               []enumDef
	firstSeen           string
}

// DomainDir is the directory the enums module scans.
func DomainDir(f *spec.File) string {
	if cfg := f.Modules["enums"]; cfg != nil {
		if v, ok := cfg["domain"].(string); ok && v != "" {
			return strings.TrimSuffix(v, "/")
		}
	}
	return DefaultDomainDir
}

// EnumsCode scans the domain package of the project for go-enum markers and generates
// the catalog, ported from taply's enumsgen.
func EnumsCode(f *spec.File, project fs.FS) ([]byte, error) {
	dir := DomainDir(f)
	entities, pkgName, err := scanEnums(project, dir)
	if err != nil {
		return nil, fmt.Errorf("module enums: %w", err)
	}

	data := struct {
		Import  string
		Package string
		Entries []enumEntity
	}{Package: pkgName, Entries: entities}
	if len(entities) > 0 {
		data.Import = f.Project.Module + "/" + dir
	}
	return renderTemplate(enumsTmpl, data)
}

func scanEnums(project fs.FS, dir string) ([]enumEntity, string, error) {
	files, err := fs.Glob(project, path.Join(dir, "*.go"))
	if err != nil {
		return nil, "", err
	}
	slices.Sort(files)

	byEntity := map[string]*enumEntity{}
	pkgName := ""
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := fs.ReadFile(project, file)
		if err != nil {
			return nil, "", err
		}
		af, err := parser.ParseFile(fset, file, src, parser.ParseComments)
		if err != nil {
			return nil, "", fmt.Errorf("parse %s: %w", file, err)
		}
		for _, decl := range af.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST || gd.Doc == nil {
				continue
			}
			marker := findMarker(gd.Doc)
			if marker == nil {
				continue
			}
			pkgName = af.Name.Name
			values, typed, err := enumValues(gd)
			if err != nil {
				return nil, "", fmt.Errorf("%s: %s.%s: %w", file, marker.Entity, marker.Enums[0].Name, err)
			}
			def := marker.Enums[0]
			def.Typed, def.Values = typed, values

			ent, ok := byEntity[marker.Entity]
			if !ok {
				marker.Enums = nil
				marker.firstSeen = fmt.Sprintf("%s (%s.%s)", file, marker.Entity, def.Name)
				byEntity[marker.Entity] = marker
				ent = marker
			} else if ent.Description != marker.Description || ent.Order != marker.Order {
				return nil, "", fmt.Errorf("%s: %s.%s declares entity=%q order=%d, but %s declared entity=%q order=%d",
					file, marker.Entity, def.Name, marker.Description, marker.Order, ent.firstSeen, ent.Description, ent.Order)
			}
			if slices.ContainsFunc(ent.Enums, func(e enumDef) bool { return e.Name == def.Name }) {
				return nil, "", fmt.Errorf("%s: duplicate go-enum %s.%s", file, marker.Entity, def.Name)
			}
			ent.Enums = append(ent.Enums, def)
		}
	}

	out := make([]enumEntity, 0, len(byEntity))
	for _, e := range byEntity {
		out = append(out, *e)
	}
	slices.SortFunc(out, func(a, b enumEntity) int {
		if a.Order != b.Order {
			return a.Order - b.Order
		}
		return strings.Compare(a.Entity, b.Entity)
	})
	return out, pkgName, nil
}

func findMarker(doc *ast.CommentGroup) *enumEntity {
	for _, c := range doc.List {
		m := enumMarker.FindStringSubmatch(c.Text)
		if m == nil {
			continue
		}
		order, _ := strconv.Atoi(m[5])
		return &enumEntity{Entity: m[1], Description: m[4], Order: order, Enums: []enumDef{{Name: m[2], Description: m[3]}}}
	}
	return nil
}

func enumValues(gd *ast.GenDecl) ([]enumValue, bool, error) {
	var (
		out   []enumValue
		typed bool
	)
	for _, spec := range gd.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		if len(vs.Names) != 1 || len(vs.Values) != 1 {
			return nil, false, errors.New("every constant of a marked block needs its own name and value")
		}
		if id, ok := vs.Type.(*ast.Ident); ok && id.Name != "string" {
			typed = true
		}
		desc := ""
		if vs.Comment != nil && len(vs.Comment.List) > 0 {
			desc = strings.TrimSpace(strings.TrimPrefix(vs.Comment.List[0].Text, "//"))
		}
		out = append(out, enumValue{Const: vs.Names[0].Name, Description: desc})
	}
	if len(out) == 0 {
		return nil, false, errors.New("a marked block has no constants")
	}
	return out, typed, nil
}

var enumsTmpl = template.Must(template.New("enums").Parse(header + `
// Package enums is the catalog of the enums of the project, built from the go-enum markers
// of its domain package.
package enums

import (
{{- if .Import}}
	"{{.Import}}"
{{- end}}
	"github.com/aidarbn/platform-go/kit/enumx"
)

// Catalog lists every enum with its values and descriptions.
var Catalog = enumx.Catalog{
{{- range .Entries}}
	{
		Entity:      {{printf "%q" .Entity}},
		Description: {{printf "%q" .Description}},
		Enums: []enumx.Enum{
{{- range .Enums}}
			{
				Name:        {{printf "%q" .Name}},
				Description: {{printf "%q" .Description}},
				Values: []enumx.Value{
{{- $typed := .Typed}}
{{- range .Values}}
					{Value: {{if $typed}}string({{$.Package}}.{{.Const}}){{else}}{{$.Package}}.{{.Const}}{{end}}, Description: {{printf "%q" .Description}}},
{{- end}}
				},
			},
{{- end}}
		},
	},
{{- end}}
}
`))
