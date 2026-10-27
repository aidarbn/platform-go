package openapi_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/aidarbn/platform-go/internal/openapi"
	"github.com/aidarbn/platform-go/internal/openapi/oascheck"
)

var audFiles = []string{
	"aud/common/v1/common.proto", "aud/tablet/v1/tablet.proto", "aud/manager/v1/manager.proto",
	"aud/admin/v1/admin.proto", "aud/public/v1/public.proto",
}

const audParam = "base=testdata/documents/base.yaml"

// generateAud runs the plugin on the audience API with the documents of a platformgo.yaml.
func generateAud(t *testing.T, files []*descriptorpb.FileDescriptorProto, config string) (map[string][]byte, string) {
	t.Helper()
	param := audParam
	if config != "" {
		path := filepath.Join(t.TempDir(), "platformgo.yaml")
		if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
		param += ",documents=" + path
	}
	resp := run(t, param, audFiles, files)
	out := map[string][]byte{}
	for _, f := range resp.GetFile() {
		out[f.GetName()] = []byte(f.GetContent())
	}
	return out, resp.GetError()
}

// The documents of the audiences of a restaurants API — the tablet, the manager, the
// superadmin, the public and the partners — each a valid description with only its
// operations and the components they use; the whole description stays as it is.
func TestGoldenDocuments(t *testing.T) {
	files := compile(t, "testdata/proto", audFiles...)
	config, err := os.ReadFile("testdata/documents/platformgo.yaml")
	if err != nil {
		t.Fatal(err)
	}
	got, errText := generateAud(t, files, string(config))
	if errText != "" {
		t.Fatal(errText)
	}
	plain, errText := generateAud(t, files, "")
	if errText != "" {
		t.Fatal(errText)
	}
	// Without documents the files are the same, and no document file is written.
	for name, b := range plain {
		if string(got[name]) != string(b) {
			t.Errorf("%s differs with documents", name)
		}
	}
	var docs []string
	for name := range got {
		if _, ok := plain[name]; !ok {
			docs = append(docs, name)
		}
	}
	slices.Sort(docs)
	want := []string{"openapi.manager.bundle.yaml", "openapi.partner.bundle.yaml", "openapi.public.bundle.yaml", "openapi.superadmin.bundle.yaml", "openapi.tablet.bundle.yaml"}
	if !slices.Equal(docs, want) {
		t.Fatalf("documents = %v, want %v", docs, want)
	}
	for _, name := range docs {
		if err := oascheck.Validate(got[name]); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.HasPrefix(string(got[name]), openapi.Header) {
			t.Errorf("%s has no generated mark: generate would not remove it", name)
		}
		checkClosed(t, name, got[name])
		golden(t, "documents/"+name, got[name])
	}
	if err := oascheck.Validate(got[openapi.BundleFile]); err != nil {
		t.Fatal(err)
	}

	all := summarize(t, got[openapi.BundleFile])
	for _, c := range []struct {
		doc        string
		operations []string
		tags       []string
		groups     []string
		has, hasnt []string // schemas
		schemes    []string
		title      string
	}{
		{
			doc:        "tablet",
			operations: []string{"TabletService_CreateOrder", "TabletService_GetMenu"},
			tags:       []string{"Tablet", "Tablet orders"}, groups: []string{"Tablet API"},
			has:     []string{"aud.common.v1.Branch", "aud.common.v1.Money", "aud.tablet.v1.OrderLine", "Error", "google.rpc.ErrorInfo"},
			hasnt:   []string{"aud.admin.v1.Restaurant", "aud.admin.v1.AuditRecord", "aud.manager.v1.Product", "Unused"},
			schemes: []string{"bearerAuth"}, title: "Restaurants API — the tablet",
		},
		{
			doc:        "manager",
			operations: []string{"BranchService_ListBranches", "ProductService_ListProducts", "ProductService_UpdateProduct"},
			tags:       []string{"Products", "Branches"}, groups: []string{"Manager API"},
			has:     []string{"aud.common.v1.Branch", "aud.common.v1.Money", "aud.manager.v1.Product"},
			hasnt:   []string{"aud.tablet.v1.Menu", "aud.admin.v1.Restaurant"},
			schemes: []string{"bearerAuth"}, title: "Restaurants API — the manager",
		},
		{
			// By service: the method whose section is in no group of the base comes too.
			doc:        "superadmin",
			operations: []string{"AdminService_ListRestaurants", "AdminService_RebuildRecommendations"},
			tags:       []string{"Superadmin", "Superadmin recommendations"}, groups: []string{"Superadmin API"},
			has:     []string{"aud.admin.v1.AuditRecord", "aud.admin.v1.Restaurant"},
			hasnt:   []string{"aud.common.v1.Money", "aud.common.v1.Branch"},
			schemes: []string{"bearerAuth"}, title: "Restaurants API — the superadmin",
		},
		{
			// An excluded tag wins over an included one.
			doc:        "public",
			operations: []string{"AuthService_Login", "StatusService_GetStatus"},
			tags:       []string{"Auth", "Status"}, groups: []string{"Public API"},
			has:     []string{"aud.public.v1.LoginRequest", "aud.public.v1.Status"},
			hasnt:   []string{"aud.common.v1.Money", "aud.tablet.v1.Menu"},
			schemes: []string{"bearerAuth"}, title: "Restaurants API",
		},
		{
			// Everything but a group: the base's title, the document's description.
			doc: "partner",
			operations: []string{
				"AuthService_Login", "BranchService_ListBranches", "ProductService_ListProducts", "ProductService_UpdateProduct",
				"StatusService_GetStatus", "TabletService_CreateOrder", "TabletService_GetMenu",
			},
			tags:   []string{"Tablet", "Tablet orders", "Products", "Branches", "Auth", "Status"},
			groups: []string{"Tablet API", "Manager API", "Public API"},
			has:    []string{"aud.common.v1.Money", "aud.tablet.v1.Menu", "aud.manager.v1.Product"},
			hasnt:  []string{"aud.admin.v1.Restaurant", "google.protobuf.Timestamp"},
			// The superadmin method in no group of the base is not excluded by the group.
			schemes: []string{"bearerAuth"}, title: "Restaurants API",
		},
	} {
		s := summarize(t, got["openapi."+c.doc+".bundle.yaml"])
		ops := slices.Clone(c.operations)
		if c.doc == "partner" {
			ops = append(ops, "AdminService_RebuildRecommendations")
			slices.Sort(ops)
		}
		if !slices.Equal(s.operations, ops) {
			t.Errorf("%s: operations %v, want %v", c.doc, s.operations, ops)
		}
		tags := c.tags
		if c.doc == "partner" {
			tags = append(slices.Clone(tags), "Superadmin recommendations")
		}
		if !sameSet(s.tags, tags) {
			t.Errorf("%s: tags %v, want %v", c.doc, s.tags, tags)
		}
		if !slices.Equal(s.groups, c.groups) {
			t.Errorf("%s: groups %v, want %v", c.doc, s.groups, c.groups)
		}
		for _, name := range c.has {
			if !slices.Contains(s.schemas, name) {
				t.Errorf("%s: schema %s is missing", c.doc, name)
			}
		}
		for _, name := range c.hasnt {
			if slices.Contains(s.schemas, name) {
				t.Errorf("%s: schema %s is not used by the document", c.doc, name)
			}
		}
		if !slices.Equal(s.schemes, c.schemes) {
			t.Errorf("%s: security schemes %v, want %v", c.doc, s.schemes, c.schemes)
		}
		if s.title != c.title {
			t.Errorf("%s: title %q, want %q", c.doc, s.title, c.title)
		}
		if s.servers != all.servers {
			t.Errorf("%s: servers %q, want the base's %q", c.doc, s.servers, all.servers)
		}
	}
	// The whole description keeps everything of the base.
	if !slices.Contains(all.schemas, "Unused") || !slices.Equal(all.schemes, []string{"bearerAuth", "legacyKey"}) {
		t.Errorf("the whole description lost the base's components: %v %v", all.schemas, all.schemes)
	}
	if again, _ := generateAud(t, files, string(config)); !equalFiles(again, got) {
		t.Error("a second run differs")
	}
}

func equalFiles(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if string(b[k]) != string(v) {
			return false
		}
	}
	return true
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

type summary struct {
	operations, tags, groups, schemas, schemes []string
	title, servers                             string
}

func summarize(t *testing.T, b []byte) summary {
	t.Helper()
	var doc struct {
		Info    struct{ Title string }  `yaml:"info"`
		Servers []struct{ URL string }  `yaml:"servers"`
		Tags    []struct{ Name string } `yaml:"tags"`
		Groups  []struct {
			Name string
		} `yaml:"x-tagGroups"`
		Paths      map[string]map[string]any `yaml:"paths"`
		Components struct {
			Schemas map[string]any `yaml:"schemas"`
			Schemes map[string]any `yaml:"securitySchemes"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	var s summary
	s.title = doc.Info.Title
	for _, sv := range doc.Servers {
		s.servers += sv.URL + " "
	}
	for _, item := range doc.Paths {
		for _, op := range item {
			if op, ok := op.(map[string]any); ok && op["operationId"] != nil {
				s.operations = append(s.operations, op["operationId"].(string))
			}
		}
	}
	slices.Sort(s.operations)
	for _, tg := range doc.Tags {
		s.tags = append(s.tags, tg.Name)
	}
	for _, g := range doc.Groups {
		s.groups = append(s.groups, g.Name)
	}
	for name := range doc.Components.Schemas {
		s.schemas = append(s.schemas, name)
	}
	slices.Sort(s.schemas)
	for name := range doc.Components.Schemes {
		s.schemes = append(s.schemes, name)
	}
	slices.Sort(s.schemes)
	return s
}

// checkClosed fails when a reference of a document names a component it does not
// have, or a tag of an operation is not among the tags.
func checkClosed(t *testing.T, name string, b []byte) {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	comps, _ := doc["components"].(map[string]any)
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok {
				rest, ok := strings.CutPrefix(ref, "#/components/")
				if !ok {
					t.Errorf("%s: a reference outside of the document: %s", name, ref)
					return
				}
				kind, key, _ := strings.Cut(rest, "/")
				if m, _ := comps[kind].(map[string]any); m == nil || m[key] == nil {
					t.Errorf("%s: %s names nothing", name, ref)
				}
			}
			for _, c := range v {
				walk(c)
			}
		case []any:
			for _, c := range v {
				walk(c)
			}
		}
	}
	walk(doc)
}

func TestDocumentErrors(t *testing.T) {
	files := compile(t, "testdata/proto", audFiles...)
	head := "modules:\n  api:\n    openapi:\n      documents:\n"
	for _, c := range []struct{ name, docs, want string }{
		{"unknown group", "        - {name: a, include: {tag_groups: [Kitchen API]}}\n", `include.tag_groups: no group "Kitchen API"`},
		{"unknown tag", "        - {name: a, exclude: {tags: [Kitchen]}}\n", `exclude.tags: no tag "Kitchen"`},
		{"short service name", "        - {name: a, include: {services: [AdminService]}}\n", `include.services: no service "AdminService"`},
		{"nothing selected", "        - {name: a, include: {tags: [Auth]}, exclude: {tag_groups: [Public API]}}\n", "document a selects no operation"},
		{"bad name", "        - {name: Tablet}\n", `name "Tablet" must be lower case`},
		{"twice", "        - {name: a}\n        - {name: a}\n", `name "a" is used twice`},
		{"all", "        - {name: all}\n", `name "all" is the whole description`},
		{"empty name in a list", "        - {name: a, include: {tags: ['']}}\n", "documents[0] include: an empty name"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, errText := generateAud(t, files, head+c.docs)
			if !strings.Contains(errText, c.want) {
				t.Errorf("error %q, want %q", errText, c.want)
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		resp := run(t, audParam+",documents=testdata/documents/nothing.yaml", audFiles, files)
		if !strings.Contains(resp.GetError(), "documents:") {
			t.Errorf("error %q", resp.GetError())
		}
	})
	t.Run("not yaml", func(t *testing.T) {
		_, errText := generateAud(t, files, "modules: [")
		if !strings.Contains(errText, "documents:") {
			t.Errorf("error %q", errText)
		}
	})
	t.Run("no documents", func(t *testing.T) {
		// A platformgo.yaml without documents gives the files of a run without the option.
		got, errText := generateAud(t, files, "modules:\n  api: {}\n")
		plain, _ := generateAud(t, files, "")
		if errText != "" || !equalFiles(got, plain) {
			t.Errorf("files differ without documents: %s", errText)
		}
	})
}

// The single layout writes the documents next to its one file.
func TestDocumentsSingleLayout(t *testing.T) {
	files := compile(t, "testdata/proto", audFiles...)
	path := filepath.Join(t.TempDir(), "platformgo.yaml")
	if err := os.WriteFile(path, []byte("modules:\n  api:\n    openapi:\n      documents:\n        - {name: tablet, include: {tag_groups: [Tablet API]}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp := run(t, audParam+",layout=single,documents="+path, audFiles, files)
	if resp.GetError() != "" {
		t.Fatal(resp.GetError())
	}
	var names []string
	for _, f := range resp.GetFile() {
		names = append(names, f.GetName())
	}
	if !slices.Equal(names, []string{"openapi.tablet.bundle.yaml", "openapi.yaml"}) {
		t.Errorf("files = %v", names)
	}
}

// A proto file whose yaml would be the bundle of a document fails generation.
func TestDocumentFileClash(t *testing.T) {
	dir := t.TempDir()
	src := "syntax = \"proto3\";\npackage x;\nimport \"google/api/annotations.proto\";\nservice S {\n  rpc M(R) returns (R) {\n    option (google.api.http) = {get: \"/v1/m\"};\n  }\n}\nmessage R {}\n"
	if err := os.WriteFile(filepath.Join(dir, "openapi.a.bundle.proto"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "platformgo.yaml")
	if err := os.WriteFile(cfg, []byte("modules:\n  api:\n    openapi:\n      documents:\n        - {name: a}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := compile(t, dir, "openapi.a.bundle.proto")
	resp := run(t, "documents="+cfg, []string{"openapi.a.bundle.proto"}, files)
	if !strings.Contains(resp.GetError(), "would be written to openapi.a.bundle.yaml") {
		t.Errorf("error %q", resp.GetError())
	}
	resp = run(t, "layout=single,path=openapi.a.bundle.yaml,documents="+cfg, []string{"openapi.a.bundle.proto"}, files)
	if !strings.Contains(resp.GetError(), "document a would be written to openapi.a.bundle.yaml") {
		t.Errorf("error %q", resp.GetError())
	}
}

// What the base says and the generator never writes survives the cut: an alias of the
// base is written in full, a path item without operations stays, a schema named by a
// discriminator by its bare name stays, an extension among the components stays.
func TestDocumentsKeepWhatTheBaseSays(t *testing.T) {
	files := compile(t, "testdata/proto", audFiles...)
	path := filepath.Join(t.TempDir(), "platformgo.yaml")
	if err := os.WriteFile(path, []byte("modules:\n  api:\n    openapi:\n      documents:\n        - {name: tablet, include: {tag_groups: [Tablet API]}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp := run(t, "base=testdata/documents/base-edges.yaml,documents="+path, audFiles, files)
	if resp.GetError() != "" {
		t.Fatal(resp.GetError())
	}
	var doc []byte
	for _, f := range resp.GetFile() {
		if f.GetName() == "openapi.tablet.bundle.yaml" {
			doc = []byte(f.GetContent())
		}
	}
	if err := oascheck.Validate(doc); err != nil {
		t.Fatalf("%v\n%s", err, doc)
	}
	checkClosed(t, "tablet", doc)
	if strings.Contains(string(doc), "*animal") || strings.Contains(string(doc), "&animal") {
		t.Error("an alias of the base is left in the document")
	}
	s := summarize(t, doc)
	for _, name := range []string{"Pet", "Dog", "Cat", "Owner"} {
		if !slices.Contains(s.schemas, name) {
			t.Errorf("schema %s is missing: %v", name, s.schemas)
		}
	}
	if slices.Contains(s.schemas, "Dropped") || strings.Contains(string(doc), "/v1/admin-pets") {
		t.Error("the superadmin part of the base is in the document")
	}
	var v struct {
		Paths      map[string]map[string]any `yaml:"paths"`
		Components map[string]any            `yaml:"components"`
	}
	if err := yaml.Unmarshal(doc, &v); err != nil {
		t.Fatal(err)
	}
	if v.Paths["/v1/legacy"]["summary"] == nil || v.Paths["/v1/pets"]["get"] == nil {
		t.Errorf("paths of the base: %v", v.Paths)
	}
	if v.Components["x-codegen"] == nil {
		t.Error("the extension among the components is gone")
	}
}
