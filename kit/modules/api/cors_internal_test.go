package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// corsCase is one request against one policy and the CORS headers it must get back.
// An empty expectation means the header must be absent.
type corsCase struct {
	name      string
	cfg       Config
	extra     []string // headers of api.CORSHeaders
	method    string
	origin    string
	preflight bool

	status      int
	allowOrigin string
	vary        string
	credentials string
	headers     string
	expose      string
	maxAge      string
	reached     bool // the request went on to the API
}

func TestCORSPolicy(t *testing.T) {
	const base = "Authorization, Content-Type, Accept, X-Request-ID, Idempotency-Key"
	exact := []string{"https://app.example.com"}
	star := []string{"*"}

	for _, c := range []corsCase{
		{
			name: "exact origin, preflight, defaults", cfg: Config{CORSOrigins: exact},
			method: "OPTIONS", origin: "https://app.example.com", preflight: true,
			status: 204, allowOrigin: "https://app.example.com", vary: "Origin", credentials: "true",
			headers: base, expose: "X-Request-ID", maxAge: "86400",
		},
		{
			name: "exact origin, plain call, defaults", cfg: Config{CORSOrigins: exact},
			method: "POST", origin: "https://app.example.com",
			status: 200, allowOrigin: "https://app.example.com", vary: "Origin", credentials: "true",
			expose: "X-Request-ID", reached: true,
		},
		{
			name: "foreign origin, preflight goes on untouched", cfg: Config{CORSOrigins: exact},
			method: "OPTIONS", origin: "https://evil.example.com", preflight: true,
			status: 200, reached: true,
		},
		{
			name: "no origin is not a CORS call", cfg: Config{CORSOrigins: star},
			method: "GET", status: 200, reached: true,
		},
		{
			name: "OPTIONS without Access-Control-Request-Method is not a preflight", cfg: Config{CORSOrigins: exact},
			method: "OPTIONS", origin: "https://app.example.com",
			status: 200, allowOrigin: "https://app.example.com", vary: "Origin", credentials: "true",
			expose: "X-Request-ID", reached: true,
		},
		{
			name: "star with credentials echoes the origin, as before", cfg: Config{CORSOrigins: star},
			method: "OPTIONS", origin: "https://any.test", preflight: true,
			status: 204, allowOrigin: "https://any.test", vary: "Origin", credentials: "true",
			headers: base, expose: "X-Request-ID", maxAge: "86400",
		},
		{
			name: "star without credentials answers a literal star", cfg: Config{CORSOrigins: star, CORSNoCredentials: true},
			method: "OPTIONS", origin: "https://any.test", preflight: true,
			status: 204, allowOrigin: "*", headers: base, expose: "X-Request-ID", maxAge: "86400",
		},
		{
			name: "star without credentials on a plain call", cfg: Config{CORSOrigins: star, CORSNoCredentials: true},
			method: "GET", origin: "http://localhost:3000",
			status: 200, allowOrigin: "*", expose: "X-Request-ID", reached: true,
		},
		{
			name: "star among other origins still answers a star", cfg: Config{CORSOrigins: []string{"https://app.example.com", "*"}, CORSNoCredentials: true},
			method: "GET", origin: "https://app.example.com",
			status: 200, allowOrigin: "*", expose: "X-Request-ID", reached: true,
		},
		{
			name: "exact origin without credentials echoes it without credentials", cfg: Config{CORSOrigins: exact, CORSNoCredentials: true},
			method: "GET", origin: "https://app.example.com",
			status: 200, allowOrigin: "https://app.example.com", vary: "Origin", expose: "X-Request-ID", reached: true,
		},
		{
			name: "project and environment headers, expose, max age",
			cfg: Config{
				CORSOrigins: exact, CORSHeaders: []string{"X-Device", "x-restaurant-id", "authorization"},
				CORSExpose: []string{"X-Total-Count", "x-request-id"}, CORSMaxAge: 600,
			},
			extra:  []string{"X-Restaurant-Id"},
			method: "OPTIONS", origin: "https://app.example.com", preflight: true,
			status: 204, allowOrigin: "https://app.example.com", vary: "Origin", credentials: "true",
			headers: base + ", X-Restaurant-Id, X-Device", expose: "X-Request-ID, X-Total-Count", maxAge: "600",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := c.cfg
			cfg.setDefaults()
			reached := false
			h := newCORS(cfg, c.extra).wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusOK)
			}))
			r := httptest.NewRequest(c.method, "/v1/echo", nil)
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			if c.preflight {
				r.Header.Set("Access-Control-Request-Method", "POST")
				r.Header.Set("Access-Control-Request-Headers", "x-restaurant-id")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			got := w.Result().Header
			if w.Code != c.status || reached != c.reached {
				t.Errorf("status %d reached %v, want %d %v", w.Code, reached, c.status, c.reached)
			}
			for header, want := range map[string]string{
				"Access-Control-Allow-Origin":      c.allowOrigin,
				"Vary":                             c.vary,
				"Access-Control-Allow-Credentials": c.credentials,
				"Access-Control-Allow-Headers":     c.headers,
				"Access-Control-Expose-Headers":    c.expose,
				"Access-Control-Max-Age":           c.maxAge,
			} {
				if g := got.Get(header); g != want {
					t.Errorf("%s = %q, want %q", header, g, want)
				}
			}
			if c.preflight && c.status == 204 && got.Get("Access-Control-Allow-Methods") != "GET, POST, PUT, PATCH, DELETE, OPTIONS" {
				t.Errorf("Allow-Methods = %q", got.Get("Access-Control-Allow-Methods"))
			}
		})
	}
}

func TestValidHeaderName(t *testing.T) {
	for name, want := range map[string]bool{
		"X-Restaurant-Id": true,
		"x_custom.1~!":    true,
		"":                false,
		"X Bad":           false,
		"X-Bad,Other":     false,
		"X-Bad\r\nSet":    false,
		"Ключ":            false,
	} {
		if got := validHeaderName(name); got != want {
			t.Errorf("%q: %v, want %v", name, got, want)
		}
	}
}
