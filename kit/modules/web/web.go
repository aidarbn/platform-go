// Package web is a platform module: server rendered pages next to the API — templ
// components, htmx fragments, CSRF protection, sealed cookies and hashed static assets
// from kit/webx. Pages are plain routes of the api module, so they share its server,
// its request ids, metrics and access log.
package web

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/platform"
	"github.com/aidarbn/platform-go/kit/webx"
)

// Config holds module settings.
type Config struct {
	// SecretKey seals cookies; at least 32 bytes. Changing it signs every visitor out of
	// their unfinished forms, nothing more.
	SecretKey []byte
	// InsecureCookies allows cookies over plain http: local development only.
	InsecureCookies bool
}

// Load reads the module settings from environment variables.
func Load(l *confx.Loader) Config {
	cfg := Config{InsecureCookies: l.Bool("WEB_INSECURE_COOKIES", false)}
	raw := l.Required("WEB_SECRET_KEY")
	if raw == "" {
		return cfg
	}
	key, err := decodeKey(raw)
	if err != nil {
		l.Fail(fmt.Errorf("%s: %w", l.Key("WEB_SECRET_KEY"), err))
		return cfg
	}
	cfg.SecretKey = key
	return cfg
}

func decodeKey(raw string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if key, err := enc.DecodeString(raw); err == nil {
			if len(key) < 32 {
				return nil, fmt.Errorf("the key is %d bytes, at least 32 are needed", len(key))
			}
			return key, nil
		}
	}
	return nil, errors.New("the key is not base64: generate one with openssl rand -base64 32")
}

// Kit is what pages use: the CSRF protection and the sealed cookies.
type Kit struct {
	CSRF    *webx.CSRF
	Cookies *webx.Cookies
	// Secure tells whether cookies are marked Secure.
	Secure bool
}

// From returns the kit from the container.
func From(app *platform.App) *Kit { return platform.Get[*Kit](app) }

// Module implements platform.Module.
type Module struct{ cfg Config }

// New builds the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements platform.Module.
func (m *Module) Name() string { return "web" }

// Init puts the kit into the container.
func (m *Module) Init(_ context.Context, app *platform.App) error {
	secure := !m.cfg.InsecureCookies
	cookies, err := webx.NewCookies(m.cfg.SecretKey, secure)
	if err != nil {
		return fmt.Errorf("web: %w", err)
	}
	if !secure {
		app.Logger().Warn("cookies go over plain http: WEB_INSECURE_COOKIES is for local development only")
	}
	platform.Provide(app, &Kit{CSRF: webx.NewCSRF(secure), Cookies: cookies, Secure: secure})
	return nil
}
