package api_test

import (
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/aidarbn/platform-go/internal/openapi"
	"github.com/aidarbn/platform-go/internal/openapi/oascheck"
	"github.com/aidarbn/platform-go/kit/confx"
	testv1 "github.com/aidarbn/platform-go/kit/internal/testapi/platformtest/v1"
	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/openapipb"
	"github.com/aidarbn/platform-go/kit/platform"
)

// describeWithDocuments generates the description of the test service the way
// make generate does, with documents of a platformgo.yaml: CreateThing gets a section
// of its own, Things, so a document can hold it alone.
func describeWithDocuments(t *testing.T, documents string) fstest.MapFS {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "platformgo.yaml")
	if err := os.WriteFile(cfg, []byte("modules:\n  api:\n    openapi:\n      documents:\n"+documents), 0o644); err != nil {
		t.Fatal(err)
	}
	req := openapi.RequestFor("documents="+cfg, testv1.File_platformtest_v1_echo_proto)
	for _, f := range req.GetProtoFile() {
		for _, s := range f.GetService() {
			for _, m := range s.GetMethod() {
				if m.GetName() == "CreateThing" {
					if m.Options == nil {
						m.Options = &descriptorpb.MethodOptions{}
					}
					proto.SetExtension(m.Options, openapipb.E_Method, &openapipb.MethodOptions{Tag: "Things"})
				}
			}
		}
	}
	files, err := openapi.Generate(req)
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{}
	for name, b := range files {
		fsys[name] = &fstest.MapFile{Data: b}
	}
	return fsys
}

func operations(t *testing.T, body string) []string {
	t.Helper()
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatal(err)
	}
	var out []string
	for p, item := range doc.Paths {
		for verb := range item {
			out = append(out, verb+" "+p)
		}
	}
	return out
}

// Every document is served at its own address with a page of its own, the whole
// description stays at /openapi.yaml; what a document describes is what the gateway
// answers there.
func TestDocsServeTheDocuments(t *testing.T) {
	fsys := describeWithDocuments(t, `        - name: things
          title: Things
          include: {tags: [Things]}
        - name: echo
          exclude: {tags: [Things]}
`)
	s := start(t, api.Config{Docs: true}, api.WithOpenAPI(fsys))

	code, all, h := s.do("GET", "/openapi.yaml", "", nil)
	if code != http.StatusOK || all != string(fsys[openapi.BundleFile].Data) || h.Get("Content-Type") != "application/yaml" {
		t.Fatalf("/openapi.yaml: %d %s", code, h)
	}
	code, things, h := s.do("GET", "/openapi/things.yaml", "", nil)
	if code != http.StatusOK || things != string(fsys[openapi.DocumentFile("things")].Data) || h.Get("Content-Type") != "application/yaml" {
		t.Fatalf("/openapi/things.yaml: %d %s", code, things)
	}
	code, echo, _ := s.do("GET", "/openapi/echo.yaml", "", nil)
	if code != http.StatusOK || echo != string(fsys[openapi.DocumentFile("echo")].Data) {
		t.Fatalf("/openapi/echo.yaml: %d", code)
	}
	if got := operations(t, things); len(got) != 1 || got[0] != "post /v1/things" {
		t.Errorf("things describes %v", got)
	}
	if n, e, a := len(operations(t, things)), len(operations(t, echo)), len(operations(t, all)); n+e != a || e == 0 {
		t.Errorf("operations: things %d + echo %d, the whole one %d", n, e, a)
	}
	if strings.Contains(echo, "/v1/things") || strings.Contains(echo, "CreateThingResponse") {
		t.Error("the echo document describes the things")
	}
	for _, doc := range []string{all, things, echo} {
		if err := oascheck.Validate([]byte(doc)); err != nil {
			t.Fatal(err)
		}
	}
	// The answer of the gateway matches the schema the document gives for it.
	check, err := oascheck.NewChecker([]byte(things))
	if err != nil {
		t.Fatal(err)
	}
	code, got, _ := s.do("POST", "/v1/things", `{"name":"a"}`, map[string]string{"Content-Type": "application/json"})
	if code != http.StatusOK {
		t.Fatalf("POST /v1/things: %d %s", code, got)
	}
	if err := check.Check("platformtest.v1.CreateThingResponse", []byte(got)); err != nil {
		t.Error(err)
	}

	for path, url := range map[string]string{"/docs": "/openapi.yaml", "/docs/things": "/openapi/things.yaml", "/docs/echo": "/openapi/echo.yaml"} {
		code, body, h := s.do("GET", path, "", nil)
		if code != http.StatusOK || !strings.Contains(body, `data-url="`+url+`"`) || !strings.HasPrefix(h.Get("Content-Type"), "text/html") {
			t.Errorf("%s: %d %s", path, code, body)
		}
	}
	for _, path := range []string{"/openapi/nothing.yaml", "/docs/nothing", "/openapi/all.yaml"} {
		if code, _, _ := s.do("GET", path, "", nil); code != http.StatusNotFound {
			t.Errorf("%s: %d", path, code)
		}
	}
}

// API_DOCS_DOCUMENTS serves only the documents it lists; all stands for the whole
// description. A name the description does not have is logged.
func TestDocsDocumentsListed(t *testing.T) {
	fsys := describeWithDocuments(t, "        - {name: things, include: {tags: [Things]}}\n        - {name: echo, exclude: {tags: [Things]}}\n")
	s := start(t, api.Config{Docs: true, DocsDocuments: []string{"things", "nothing"}}, api.WithOpenAPI(fsys))
	for path, want := range map[string]int{
		"/openapi/things.yaml": http.StatusOK, "/docs/things": http.StatusOK,
		"/openapi/echo.yaml": http.StatusNotFound, "/docs/echo": http.StatusNotFound,
		"/openapi.yaml": http.StatusNotFound, "/docs": http.StatusNotFound,
	} {
		if code, _, _ := s.do("GET", path, "", nil); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
	if logs := s.logs.String(); !strings.Contains(logs, "API_DOCS_DOCUMENTS names a document the description does not have") || !strings.Contains(logs, "nothing") {
		t.Errorf("no warning about the unknown document:\n%s", logs)
	}

	s = start(t, api.Config{Docs: true, DocsDocuments: []string{api.AllDocuments}}, api.WithOpenAPI(fsys))
	for path, want := range map[string]int{"/openapi.yaml": http.StatusOK, "/docs": http.StatusOK, "/openapi/things.yaml": http.StatusNotFound} {
		if code, _, _ := s.do("GET", path, "", nil); code != want {
			t.Errorf("all: %s: %d, want %d", path, code, want)
		}
	}
	// API_DOCS off serves none of them.
	s = start(t, api.Config{Docs: false, DocsDocuments: []string{"things"}}, api.WithOpenAPI(fsys))
	if code, _, _ := s.do("GET", "/openapi/things.yaml", "", nil); code != http.StatusNotFound {
		t.Errorf("API_DOCS=false: %d", code)
	}
}

// DocsAccess decides who reads a description: here the things are public and the rest
// needs a key.
func TestDocsAccess(t *testing.T) {
	fsys := describeWithDocuments(t, "        - {name: things, include: {tags: [Things]}}\n")
	var seen []string
	extraWire = func(app *platform.App) {
		api.DocsAccess(app, func(doc string, next http.Handler) http.Handler {
			seen = append(seen, doc)
			if doc == "things" {
				return next
			}
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Docs-Key") != "secret" {
					http.Error(w, "no", http.StatusUnauthorized)
					return
				}
				next.ServeHTTP(w, r)
			})
		})
	}
	t.Cleanup(func() { extraWire = nil })
	s := start(t, api.Config{Docs: true}, api.WithOpenAPI(fsys))
	for _, c := range []struct {
		path, key string
		want      int
	}{
		{"/openapi/things.yaml", "", http.StatusOK},
		{"/docs/things", "", http.StatusOK},
		{"/openapi.yaml", "", http.StatusUnauthorized},
		{"/docs", "", http.StatusUnauthorized},
		{"/openapi.yaml", "secret", http.StatusOK},
	} {
		if code, _, _ := s.do("GET", c.path, "", map[string]string{"X-Docs-Key": c.key}); code != c.want {
			t.Errorf("%s key %q: %d, want %d", c.path, c.key, code, c.want)
		}
	}
	if strings.Join(seen, ",") != "all,all,things,things" {
		t.Errorf("DocsAccess saw %v", seen)
	}
}

func TestDocsSettingsFromEnvironment(t *testing.T) {
	if cfg := api.Load(confx.New("")); !cfg.Docs || cfg.DocsDocuments != nil {
		t.Errorf("defaults changed: %v %v", cfg.Docs, cfg.DocsDocuments)
	}
	t.Setenv("API_DOCS_DOCUMENTS", "public, tablet")
	l := confx.New("")
	if cfg := api.Load(l); l.Err() != nil || !slices.Equal(cfg.DocsDocuments, []string{"public", "tablet"}) {
		t.Errorf("%v %v", cfg.DocsDocuments, l.Err())
	}
}
