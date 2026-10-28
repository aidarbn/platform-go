package api_test

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/platform"
)

func TestCORSSettingsFromEnvironment(t *testing.T) {
	cfg := api.Load(confx.New(""))
	if cfg.CORSNoCredentials || cfg.CORSMaxAge != 86400 || cfg.CORSHeaders != nil || cfg.CORSExpose != nil {
		t.Errorf("defaults changed: %+v", cfg)
	}

	t.Setenv("API_CORS_ORIGINS", "*")
	t.Setenv("API_CORS_HEADERS", "X-Device, X-Restaurant-Id")
	t.Setenv("API_CORS_EXPOSE", "X-Total-Count")
	t.Setenv("API_CORS_CREDENTIALS", "false")
	t.Setenv("API_CORS_MAX_AGE", "600")
	l := confx.New("")
	cfg = api.Load(l)
	if l.Err() != nil || !cfg.CORSNoCredentials || cfg.CORSMaxAge != 600 ||
		!slices.Equal(cfg.CORSHeaders, []string{"X-Device", "X-Restaurant-Id"}) || !slices.Equal(cfg.CORSExpose, []string{"X-Total-Count"}) {
		t.Errorf("%+v %v", cfg, l.Err())
	}

	for key, value := range map[string]string{
		"API_CORS_HEADERS":     "X Device",
		"API_CORS_EXPOSE":      "X-Total-Count;",
		"API_CORS_MAX_AGE":     "0",
		"API_CORS_CREDENTIALS": "maybe",
	} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			l := confx.New("")
			api.Load(l)
			if l.Err() == nil || !strings.Contains(l.Err().Error(), key) {
				t.Errorf("%s=%q accepted: %v", key, value, l.Err())
			}
		})
	}
}

// The headers a project registers in code reach the preflight of the running API, next
// to the platform's and those of the environment, and the API answers a literal "*"
// in taply's mode.
func TestCORSHeadersOfTheProject(t *testing.T) {
	extraWire = func(app *platform.App) { api.CORSHeaders(app, "X-Restaurant-Id") }
	t.Cleanup(func() { extraWire = nil })
	s := start(t, api.Config{CORSOrigins: []string{"*"}, CORSNoCredentials: true, CORSHeaders: []string{"X-Device"}})

	code, _, h := s.do("OPTIONS", "/v1/echo", "", map[string]string{
		"Origin": "https://tablet.example.com", "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "x-restaurant-id",
	})
	want := "Authorization, Content-Type, Accept, X-Request-ID, Idempotency-Key, X-Restaurant-Id, X-Device"
	if code != http.StatusNoContent || h.Get("Access-Control-Allow-Headers") != want ||
		h.Get("Access-Control-Allow-Origin") != "*" || h.Get("Access-Control-Allow-Credentials") != "" {
		t.Errorf("preflight: %d %v", code, h)
	}

	code, _, h = s.do("POST", "/v1/echo", `{"message_text":"x"}`, map[string]string{"Origin": "https://tablet.example.com"})
	if code != http.StatusOK || h.Get("Access-Control-Allow-Origin") != "*" || h.Get("Access-Control-Expose-Headers") != "X-Request-ID" {
		t.Errorf("call: %d %v", code, h)
	}
	if strings.Contains(s.logs.String(), "API_CORS_ORIGINS has *") {
		t.Error("warned about a star without credentials")
	}
}

func TestCORSStarWithCredentialsWarns(t *testing.T) {
	s := start(t, api.Config{CORSOrigins: []string{"*"}})
	_, _, h := s.do("POST", "/v1/echo", `{"message_text":"x"}`, map[string]string{"Origin": "https://any.test"})
	if h.Get("Access-Control-Allow-Origin") != "https://any.test" || h.Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("the origin is no longer echoed: %v", h)
	}
	if !strings.Contains(s.logs.String(), "API_CORS_ORIGINS has * with credentials") {
		t.Errorf("no warning:\n%s", s.logs.String())
	}
}

func TestCORSHeadersRefusesABadName(t *testing.T) {
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "X Restaurant") {
			t.Errorf("panic = %v", r)
		}
	}()
	api.CORSHeaders(platform.NewApp(slog.Default()), "X Restaurant")
}

// A Config written in code is not read by Load, so the start checks its header names.
func TestCORSBadHeaderInConfigStopsTheStart(t *testing.T) {
	for _, cfg := range []api.Config{
		{CORSOrigins: []string{"*"}, CORSHeaders: []string{"X-A, X-B"}},
		{CORSOrigins: []string{"*"}, CORSExpose: []string{"X-Total\r\nSet-Cookie: x"}},
	} {
		cfg.GRPCAddr, cfg.HTTPAddr = "127.0.0.1:0", "127.0.0.1:0"
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := platform.RunContext(ctx, platform.Config{Service: "echo", OpsAddr: "127.0.0.1:0", ShutdownTimeout: time.Second},
			[]platform.Module{api.New(cfg)}, func(*platform.App) error { return nil })
		cancel()
		if err == nil || !strings.Contains(err.Error(), "is not a header name") {
			t.Errorf("%+v: %v", cfg, err)
		}
	}
}

// A preflight the CORS policy does not answer goes on to the API by default, and with
// CORSPreflightNoContent answers 204 without CORS headers, as taply's.
func TestCORSPreflightNoContent(t *testing.T) {
	if api.Load(confx.New("")).CORSPreflightNoContent {
		t.Fatal("on by default")
	}
	t.Setenv("API_CORS_PREFLIGHT_NO_CONTENT", "true")
	if !api.Load(confx.New("")).CORSPreflightNoContent {
		t.Fatal("API_CORS_PREFLIGHT_NO_CONTENT is not read")
	}

	preflight := func(origin string) map[string]string {
		h := map[string]string{"Access-Control-Request-Method": "POST"}
		if origin != "" {
			h["Origin"] = origin
		}
		return h
	}
	for name, tc := range map[string]struct {
		cfg     api.Config
		origin  string
		code    int
		allowed bool
	}{
		"default, foreign origin": {api.Config{CORSOrigins: []string{"https://app.example.com"}}, "https://evil.example.org", 0, false},
		"foreign origin":          {api.Config{CORSOrigins: []string{"https://app.example.com"}, CORSPreflightNoContent: true}, "https://evil.example.org", http.StatusNoContent, false},
		"no origin":               {api.Config{CORSOrigins: []string{"https://app.example.com"}, CORSPreflightNoContent: true}, "", http.StatusNoContent, false},
		"no CORS at all":          {api.Config{CORSPreflightNoContent: true}, "https://app.example.com", http.StatusNoContent, false},
		"allowed origin":          {api.Config{CORSOrigins: []string{"https://app.example.com"}, CORSPreflightNoContent: true}, "https://app.example.com", http.StatusNoContent, true},
	} {
		t.Run(name, func(t *testing.T) {
			s := start(t, tc.cfg)
			code, body, h := s.do("OPTIONS", "/v1/things", "", preflight(tc.origin))
			if tc.code == 0 {
				// The gateway's answer, as before the option.
				if code == http.StatusNoContent || body == "" {
					t.Errorf("the default changed: %d %q", code, body)
				}
				return
			}
			if code != tc.code || body != "" {
				t.Errorf("%d %q", code, body)
			}
			if got := h.Get("Access-Control-Allow-Origin") != ""; got != tc.allowed {
				t.Errorf("Access-Control-Allow-Origin = %q", h.Get("Access-Control-Allow-Origin"))
			}
			if !tc.allowed && h.Get("Access-Control-Allow-Methods") != "" {
				t.Error("CORS headers for a refused preflight")
			}
			// Other methods still reach the API.
			if code, _, _ := s.do("POST", "/v1/things", `{"name":"a"}`, map[string]string{"Origin": tc.origin}); code != http.StatusOK {
				t.Errorf("POST: %d", code)
			}
		})
	}
}
