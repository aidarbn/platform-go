// Package webx serves server rendered pages: templ components rendered as a whole page or
// as an htmx fragment, CSRF protection, sealed cookies for the state of multi step forms,
// static assets with content hashed names and a strict content security policy.
//
// Pages call the same use cases as the API. Nothing in the browser holds state: forms
// work without JavaScript, and htmx only replaces the part of the page that changed.
package webx

import (
	"bytes"
	"net/http"

	"github.com/a-h/templ"
)

// IsHTMX reports whether htmx sent the request, which then wants a fragment rather than
// the whole page.
func IsHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// Render writes the page, or only the fragment when htmx asked for it, with the status.
// One route serves both, so a form needs no second set of endpoints. The component is
// rendered before anything is written: a rendering error still answers 500.
func Render(w http.ResponseWriter, r *http.Request, status int, page, fragment templ.Component) error {
	c := page
	if IsHTMX(r) && fragment != nil {
		c = fragment
	}
	var buf bytes.Buffer
	if err := c.Render(r.Context(), &buf); err != nil {
		http.Error(w, "the page could not be rendered", http.StatusInternalServerError)
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Add("Vary", "HX-Request")
	w.WriteHeader(status)
	_, err := buf.WriteTo(w)
	return err
}

// Redirect sends the browser on: a plain redirect after a form post without JavaScript,
// HX-Redirect for htmx, which would otherwise swap the target page into the fragment.
func Redirect(w http.ResponseWriter, r *http.Request, url string) {
	if IsHTMX(r) {
		w.Header().Set("HX-Redirect", url)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}
