package webx

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

// Assets serves embedded static files under names that carry a hash of the content:
// app.css becomes app.1a2b3c4d.css. A changed file gets a new name, so every file can be
// cached for a year and a release never shows stale styles.
type Assets struct {
	prefix string
	byName map[string]string // app.css → app.1a2b3c4d.css
	files  map[string]asset  // app.1a2b3c4d.css → content
}

type asset struct {
	content     []byte
	contentType string
}

// NewAssets reads every file of fsys. prefix is the URL path they are served under, such
// as /static/.
func NewAssets(fsys fs.FS, prefix string) (*Assets, error) {
	a := &Assets{prefix: "/" + strings.Trim(prefix, "/") + "/", byName: map[string]string{}, files: map[string]asset{}}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		ext := path.Ext(p)
		hashed := strings.TrimSuffix(p, ext) + "." + hex.EncodeToString(sum[:4]) + ext
		contentType := mime.TypeByExtension(ext)
		if contentType == "" {
			contentType = http.DetectContentType(content)
		}
		a.byName[p] = hashed
		a.files[hashed] = asset{content: content, contentType: contentType}
		return nil
	})
	return a, err
}

// Path returns the URL of a file for a template. An unknown name panics at the first
// render, so a typo is found in a test, not by a visitor.
func (a *Assets) Path(name string) string {
	hashed, ok := a.byName[strings.TrimPrefix(name, "/")]
	if !ok {
		panic(fmt.Sprintf("webx: no asset %q", name))
	}
	return a.prefix + hashed
}

// Pattern is the route of the handler: GET /static/.
func (a *Assets) Pattern() string { return "GET " + a.prefix }

// Handler serves the files with a year long immutable cache.
func (a *Assets) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := a.files[strings.TrimPrefix(r.URL.Path, a.prefix)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", f.contentType)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(f.content)
	})
}
