package webx_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/a-h/templ"

	"github.com/aidarbn/platform-go/kit/webx"
)

func text(s string) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, s)
		return err
	})
}

func TestRenderPageOrFragment(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = webx.Render(w, r, http.StatusUnprocessableEntity, text("<html>page</html>"), text("<form>fragment</form>"))
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", nil))
	if rec.Code != 422 || rec.Body.String() != "<html>page</html>" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Errorf("page: %d %q", rec.Code, rec.Body.String())
	}
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("HX-Request", "true")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Body.String() != "<form>fragment</form>" || rec.Header().Get("Vary") != "HX-Request" {
		t.Errorf("fragment: %q %v", rec.Body.String(), rec.Header())
	}

	rec = httptest.NewRecorder()
	webx.Redirect(rec, req, "/done")
	if rec.Header().Get("HX-Redirect") != "/done" || rec.Code != http.StatusNoContent {
		t.Errorf("htmx redirect: %d %v", rec.Code, rec.Header())
	}
}

func TestCSRF(t *testing.T) {
	csrf := webx.NewCSRF(false)
	h := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, webx.CSRFToken(r.Context()))
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || rec.Body.String() != cookies[0].Value || !cookies[0].HttpOnly {
		t.Fatalf("token issue: %v %q", cookies, rec.Body.String())
	}
	token := cookies[0].Value

	post := func(form url.Values, header map[string]string) int {
		req := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookies[0])
		for k, v := range header {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post(url.Values{}, nil); code != http.StatusForbidden {
		t.Errorf("no token: %d", code)
	}
	if code := post(url.Values{webx.CSRFField: {"forged"}}, nil); code != http.StatusForbidden {
		t.Errorf("wrong token: %d", code)
	}
	if code := post(url.Values{webx.CSRFField: {token}}, nil); code != http.StatusOK {
		t.Errorf("form token: %d", code)
	}
	if code := post(url.Values{}, map[string]string{webx.CSRFHeader: token}); code != http.StatusOK {
		t.Errorf("header token: %d", code)
	}
	if code := post(url.Values{webx.CSRFField: {token}}, map[string]string{"Sec-Fetch-Site": "cross-site"}); code != http.StatusForbidden {
		t.Errorf("cross site with a stolen token: %d", code)
	}

	var input strings.Builder
	ctxRec := httptest.NewRecorder()
	csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = webx.CSRFInput().Render(r.Context(), &input)
	})).ServeHTTP(ctxRec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(input.String(), `name="csrf_token"`) {
		t.Errorf("input: %s", input.String())
	}
}

func TestCookies(t *testing.T) {
	secret := []byte(strings.Repeat("k", 32))
	c, err := webx.NewCookies(secret, true)
	if err != nil {
		t.Fatal(err)
	}
	type step struct {
		Phone string
		Code  int
	}
	rec := httptest.NewRecorder()
	if err := c.Set(rec, "step", step{Phone: "+77011234567", Code: 2}, time.Minute); err != nil {
		t.Fatal(err)
	}
	cookie := rec.Result().Cookies()[0]
	if strings.Contains(cookie.Value, "7701") || !cookie.Secure || !cookie.HttpOnly {
		t.Errorf("cookie is readable or not secure: %+v", cookie)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)
	var got step
	if !c.Get(req, "step", &got) || got.Phone != "+77011234567" {
		t.Errorf("round trip: %+v", got)
	}

	renamed := httptest.NewRequest("GET", "/", nil)
	renamed.AddCookie(&http.Cookie{Name: "admin", Value: cookie.Value})
	if c.Get(renamed, "admin", &got) {
		t.Error("a cookie moved to another name opens")
	}
	tampered := httptest.NewRequest("GET", "/", nil)
	tampered.AddCookie(&http.Cookie{Name: "step", Value: cookie.Value[:len(cookie.Value)-2] + "AA"})
	if c.Get(tampered, "step", &got) {
		t.Error("a tampered cookie opens")
	}
	other, _ := webx.NewCookies([]byte(strings.Repeat("x", 32)), true)
	if other.Get(req, "step", &got) {
		t.Error("another secret opens the cookie")
	}
	if _, err := webx.NewCookies([]byte("short"), true); err == nil {
		t.Error("a short secret is accepted")
	}
}

func TestAssets(t *testing.T) {
	a, err := webx.NewAssets(fstest.MapFS{
		"app.css":        {Data: []byte("body{}")},
		"js/htmx.min.js": {Data: []byte("htmx")},
	}, "/static/")
	if err != nil {
		t.Fatal(err)
	}
	css := a.Path("app.css")
	if !strings.HasPrefix(css, "/static/app.") || !strings.HasSuffix(css, ".css") || len(css) != len("/static/app.12345678.css") {
		t.Fatalf("path %q", css)
	}
	mux := http.NewServeMux()
	mux.Handle(a.Pattern(), a.Handler())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", css, nil))
	if rec.Body.String() != "body{}" || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") {
		t.Errorf("serve: %d %v", rec.Code, rec.Header())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/static/app.css", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("an unhashed name is served: %d", rec.Code)
	}
	defer func() {
		if recover() == nil {
			t.Error("an unknown asset does not panic")
		}
	}()
	a.Path("nope.css")
}

func TestLocale(t *testing.T) {
	supported := []string{"ru", "kk"}
	for header, want := range map[string]string{
		"kk-KZ,kk;q=0.9,ru;q=0.8": "kk",
		"en-US,en;q=0.9":          "ru",
		"":                        "ru",
		"ru-RU":                   "ru",
	} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("Accept-Language", header)
		if got := webx.Locale(req, supported, "ru"); got != want {
			t.Errorf("%q: %s, want %s", header, got, want)
		}
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Language", "ru")
	req.AddCookie(&http.Cookie{Name: webx.LocaleCookie, Value: "kk"})
	if got := webx.Locale(req, supported, "ru"); got != "kk" {
		t.Errorf("cookie choice: %s", got)
	}
}
