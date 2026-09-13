// Package i18n is a platform module: translations of API responses and error messages,
// ported from taply. Requires postgres and api.
package i18n

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/i18nx"
	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/platform"
)

// Config holds module settings. Load fills it from the environment; the platform
// generator writes the Load call into the project's config.gen.go.
type Config struct {
	DefaultLocale i18nx.Locale   // language of the base columns and the fallback
	Locales       []i18nx.Locale // languages the API answers in
}

// Load reads the module settings from environment variables.
func Load(l *confx.Loader) Config {
	cfg := Config{DefaultLocale: i18nx.Locale(l.String("I18N_DEFAULT_LOCALE", "ru"))}
	for _, code := range l.Strings("I18N_LOCALES", []string{"ru", "kk", "en"}) {
		cfg.Locales = append(cfg.Locales, i18nx.Locale(strings.ToLower(code)))
	}
	return cfg
}

// Option configures the module.
type Option func(*Module)

// WithMessages gives the module the static translations of the project. The generated
// wiring passes the embedded i18n/messages.yaml.
func WithMessages(yaml string) Option { return func(m *Module) { m.messages = yaml } }

// Module implements platform.Module.
type Module struct {
	cfg      Config
	messages string
	client   *i18nx.Client

	mu   sync.RWMutex
	skip func(context.Context) bool
}

// New creates the module.
func New(cfg Config, opts ...Option) *Module {
	m := &Module{cfg: cfg}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func (m *Module) Name() string { return "i18n" }

// Init creates the translations table, loads the static translations and puts the
// translation of requests and responses into the API.
func (m *Module) Init(ctx context.Context, app *platform.App) error {
	pool, ok := platform.Lookup[*pgxpool.Pool](app)
	if !ok {
		return errors.New("no database in the container: enable the postgres module")
	}
	if err := i18nx.EnsureSchema(ctx, pool); err != nil {
		return err
	}

	m.client = i18nx.New(pool, m.cfg.DefaultLocale, m.cfg.Locales...)
	if err := m.client.LoadMessages([]byte(i18nx.Builtin)); err != nil {
		return fmt.Errorf("i18n: platform messages: %w", err)
	}
	if strings.TrimSpace(m.messages) != "" {
		if err := m.client.LoadMessages([]byte(m.messages)); err != nil {
			return fmt.Errorf("i18n/messages.yaml: %w", err)
		}
	}

	platform.Provide(app, m)
	platform.Provide(app, m.client)
	api.AddUnaryInterceptor(app, m.client.ResponseInterceptor(m.skipCall))
	api.AddUnaryInterceptor(app, m.client.RequestInterceptor())
	return nil
}

func (m *Module) skipCall(ctx context.Context) bool {
	m.mu.RLock()
	skip := m.skip
	m.mu.RUnlock()
	return skip != nil && skip(ctx)
}

// From returns the translations client from the container.
func From(app *platform.App) *i18nx.Client { return platform.Get[*i18nx.Client](app) }

// SkipTranslation turns response substitution off for the calls fn picks — taply does it
// for tablets of a restaurant with translations disabled. Error messages are still
// translated.
func SkipTranslation(app *platform.App, fn func(ctx context.Context) bool) {
	m := platform.Get[*Module](app)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.skip = fn
}
