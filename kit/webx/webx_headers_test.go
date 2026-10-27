package webx_test

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/a-h/templ"

	"github.com/aidarbn/platform-go/kit/webx"
)

func TestPageHeaders(t *testing.T) {
	called := false
	h := webx.PageHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	want := map[string]string{
		"Content-Security-Policy": webx.ContentSecurityPolicy,
		"Referrer-Policy":         "same-origin",
		"X-Frame-Options":         "DENY",
		"X-Content-Type-Options":  "nosniff",
		"Permissions-Policy":      "camera=(), microphone=(), geolocation=()",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if !called {
		t.Error("the page handler was not called")
	}
}

// NoReferrer wraps a page that already has the page headers and tightens them.
func TestNoReferrerOverridesThePagePolicy(t *testing.T) {
	h := webx.PageHeaders(webx.NoReferrer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/reset/secret-token", nil))
	if rec.Header().Get("Referrer-Policy") != "no-referrer" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers = %v", rec.Header())
	}
}

func TestCookiesClear(t *testing.T) {
	for _, secure := range []bool{true, false} {
		c, err := webx.NewCookies([]byte(strings.Repeat("k", 32)), secure)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		c.Clear(rec, "step")
		cookies := rec.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("cookies = %v", cookies)
		}
		got := cookies[0]
		if got.Name != "step" || got.Value != "" || got.MaxAge >= 0 || !got.HttpOnly || got.Secure != secure || got.Path != "/" {
			t.Errorf("secure %v: cookie = %+v", secure, got)
		}
	}
}

func TestCookiesRejectBrokenInput(t *testing.T) {
	c, err := webx.NewCookies([]byte(strings.Repeat("k", 32)), true)
	if err != nil {
		t.Fatal(err)
	}
	// A value JSON cannot hold is refused before anything is written.
	rec := httptest.NewRecorder()
	if err := c.Set(rec, "step", func() {}, time.Minute); err == nil || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("Set of a func: %v, %v", err, rec.Result().Cookies())
	}

	read := func(value string) bool {
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(&http.Cookie{Name: "step", Value: value})
		var v any
		return c.Get(r, "step", &v)
	}
	for name, value := range map[string]string{
		"not base64": "***",
		"too short":  base64.RawURLEncoding.EncodeToString([]byte("abc")),
		"not sealed": base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 40))),
	} {
		if read(value) {
			t.Errorf("%s: accepted", name)
		}
	}
	// A sealed cookie whose value does not fit the target is refused too.
	rec = httptest.NewRecorder()
	if err := c.Set(rec, "step", "text", time.Minute); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(rec.Result().Cookies()[0])
	var n int
	if c.Get(r, "step", &n) {
		t.Error("a string was read into an int")
	}
}

func TestRenderFailureAnswers500(t *testing.T) {
	broken := templ.ComponentFunc(func(context.Context, io.Writer) error { return errors.New("broken template") })
	rec := httptest.NewRecorder()
	err := webx.Render(rec, httptest.NewRequest("GET", "/", nil), http.StatusOK, broken, nil)
	if err == nil || rec.Code != http.StatusInternalServerError {
		t.Fatalf("Render = %v, code %d", err, rec.Code)
	}
}

func TestRedirectWithoutHTMX(t *testing.T) {
	rec := httptest.NewRecorder()
	webx.Redirect(rec, httptest.NewRequest("POST", "/form", nil), "/done")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/done" {
		t.Fatalf("redirect = %d %v", rec.Code, rec.Header())
	}
}

// unreadableFile lists app.js but cannot open it. It wraps the map instead of
// embedding it, so the map's ReadFile does not bypass Open.
type unreadableFile struct{ files fstest.MapFS }

func (u unreadableFile) Open(name string) (fs.File, error) {
	if name == "app.js" {
		return nil, fs.ErrPermission
	}
	return u.files.Open(name)
}

func TestAssetsEdgeCases(t *testing.T) {
	// A file without a known extension gets its type from its content.
	a, err := webx.NewAssets(fstest.MapFS{"LICENSE": {Data: []byte("plain text license")}}, "static")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, httptest.NewRequest("GET", a.Path("LICENSE"), nil))
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("LICENSE: %d %v", rec.Code, rec.Header())
	}

	if _, err := webx.NewAssets(unreadableFile{fstest.MapFS{"app.js": {Data: []byte("x")}}}, "static"); err == nil {
		t.Error("an unreadable file was accepted")
	}
}
