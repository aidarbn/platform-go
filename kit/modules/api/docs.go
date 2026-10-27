package api

import (
	"html"
	"io/fs"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/aidarbn/platform-go/kit/platform"
)

// AllDocuments names the whole description, /openapi.yaml and /docs, among the
// documents of API_DOCS_DOCUMENTS and to DocsAccess.
const AllDocuments = "all"

// DocsAccess guards the descriptions the module serves: wrap gets the name of a
// document, AllDocuments for the whole one, and the handler of its description or of
// its page, and returns the handler to serve instead — one that asks for a password, a
// token or a role, or the same handler for a public document. API_DOCS and
// API_DOCS_DOCUMENTS decide what is served at all. A later call replaces an earlier one.
//
//	api.DocsAccess(app, func(doc string, next http.Handler) http.Handler {
//		if doc == "public" {
//			return next
//		}
//		return staffOnly(next)
//	})
func DocsAccess(app *platform.App, wrap func(document string, next http.Handler) http.Handler) {
	if wrap == nil {
		panic("api: DocsAccess needs a function; return next for a public document")
	}
	r := registry(app)
	r.add(func() { r.docsAccess = wrap })
}

// documentFile is the bundle of a document the platform's generator writes for
// api.openapi.documents of platformgo.yaml: openapi.<name>.bundle.yaml.
var documentFile = regexp.MustCompile(`^openapi\.([a-z0-9][a-z0-9_-]*)\.bundle\.yaml$`)

// documents are the names of the documents in the description folder, sorted.
func documents(fsys fs.FS) []string {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		// openapi.all.bundle.yaml is no document: all is the whole description.
		if m := documentFile.FindStringSubmatch(e.Name()); m != nil && !e.IsDir() && m[1] != AllDocuments {
			out = append(out, m[1])
		}
	}
	slices.Sort(out)
	return out
}

// serveDocs adds the descriptions to the mux: the whole one at /openapi.yaml with its
// page at /docs, and every document at /openapi/<name>.yaml with its page at
// /docs/<name>, those of API_DOCS_DOCUMENTS when it is set.
func (m *Module) serveDocs(mux *http.ServeMux) {
	docs := documents(m.openapi)
	listed := m.cfg.DocsDocuments
	for _, name := range listed {
		if name != AllDocuments && !slices.Contains(docs, name) {
			m.log.Warn("API_DOCS_DOCUMENTS names a document the description does not have", "document", name, "documents", strings.Join(docs, ","))
		}
	}
	served := func(name string) bool { return len(listed) == 0 || slices.Contains(listed, name) }
	handle := func(doc, pattern string, h http.Handler) {
		if m.registry.docsAccess != nil {
			if h = m.registry.docsAccess(doc, h); h == nil {
				panic("api: the DocsAccess function returned no handler for document " + doc)
			}
		}
		path := strings.TrimPrefix(pattern, "GET ")
		mux.Handle(pattern, withRoute(path, h))
	}
	if served(AllDocuments) {
		// The platform's generator writes the description in parts and in one file,
		// the bundle; other generators write openapi.yaml alone.
		spec, err := fs.ReadFile(m.openapi, "openapi.bundle.yaml")
		if err != nil {
			spec, err = fs.ReadFile(m.openapi, "openapi.yaml")
		}
		if err == nil {
			handle(AllDocuments, "GET /openapi.yaml", yamlHandler(spec))
			handle(AllDocuments, "GET /docs", http.HandlerFunc(docsPage))
		}
	}
	for _, name := range docs {
		if !served(name) {
			continue
		}
		spec, err := fs.ReadFile(m.openapi, "openapi."+name+".bundle.yaml")
		if err != nil {
			m.log.Warn("the description of a document cannot be read", "document", name, "error", err)
			continue
		}
		url := "/openapi/" + name + ".yaml"
		handle(name, "GET "+url, yamlHandler(spec))
		handle(name, "GET /docs/"+name, docsPageOf(url, "API — "+name))
	}
}

func yamlHandler(spec []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(spec)
	})
}

// docsPage renders the whole OpenAPI description with Scalar. The page loads the
// renderer from a CDN; the description itself is served by the service at
// /openapi.yaml.
func docsPage(w http.ResponseWriter, r *http.Request) {
	docsPageOf("/openapi.yaml", "API").ServeHTTP(w, r)
}

// docsPageOf renders the description at url.
func docsPageOf(url, title string) http.Handler {
	page := []byte(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>` + html.EscapeString(title) + `</title>
</head>
<body>
<script id="api-reference" data-url="` + html.EscapeString(url) + `"></script>
<script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
</body>
</html>
`)
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	})
}
