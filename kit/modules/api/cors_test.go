package api_test

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

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
