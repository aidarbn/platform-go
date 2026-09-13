// Package enums is a platform module: the catalog of the enums of the project served to
// clients, as taply's GetEnums does, with descriptions translated when i18n is enabled.
// Requires api.
package enums

import (
	"context"
	"encoding/json"
	"net/http"

	"google.golang.org/grpc/metadata"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/enumx"
	"github.com/aidarbn/platform-go/kit/i18nx"
	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/platform"
)

// Config holds module settings. Load fills it from the environment; the platform
// generator writes the Load call into the project's config.gen.go.
type Config struct {
	Path string // where the catalog is served
}

// Load reads the module settings from environment variables.
func Load(l *confx.Loader) Config {
	return Config{Path: l.String("ENUMS_PATH", "/v1/enums")}
}

// Option configures the module.
type Option func(*Module)

// WithCatalog gives the module the generated catalog of the project.
func WithCatalog(c enumx.Catalog) Option { return func(m *Module) { m.catalog = c } }

// Module implements platform.Module.
type Module struct {
	cfg     Config
	catalog enumx.Catalog
	app     *platform.App
}

// New creates the module.
func New(cfg Config, opts ...Option) *Module {
	if cfg.Path == "" {
		cfg.Path = "/v1/enums"
	}
	m := &Module{cfg: cfg}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func (m *Module) Name() string { return "enums" }

// Init puts the catalog into the container and serves it.
func (m *Module) Init(_ context.Context, app *platform.App) error {
	m.app = app
	platform.Provide(app, m.catalog)
	api.HandleHTTP(app, "GET "+m.cfg.Path, http.HandlerFunc(m.serve))
	return nil
}

// From returns the catalog from the container.
func From(app *platform.App) enumx.Catalog { return platform.Get[enumx.Catalog](app) }

type response struct {
	Entities enumx.Catalog `json:"entities"`
}

func (m *Module) serve(w http.ResponseWriter, r *http.Request) {
	catalog := m.catalog
	if client, ok := platform.Lookup[*i18nx.Client](m.app); ok {
		ctx := metadata.NewIncomingContext(r.Context(), metadata.Pairs("accept-language", r.Header.Get("Accept-Language")))
		catalog = Translate(catalog, client, client.ResolveLocale(ctx))
	}
	if catalog == nil {
		catalog = enumx.Catalog{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response{Entities: catalog})
}

// Translate returns the catalog with its descriptions in a locale. The keys are
// enum.<entity>, enum.<entity>.<enum> and enum.<entity>.<enum>.<value>; a missing
// translation keeps the description from the code.
func Translate(c enumx.Catalog, client *i18nx.Client, locale i18nx.Locale) enumx.Catalog {
	tr := func(key, fallback string) string {
		if text := client.Static(key, locale); text != key {
			return text
		}
		return fallback
	}
	out := make(enumx.Catalog, len(c))
	for i, e := range c {
		key := "enum." + e.Entity
		e.Description = tr(key, e.Description)
		enums := make([]enumx.Enum, len(e.Enums))
		for j, en := range e.Enums {
			enKey := key + "." + en.Name
			en.Description = tr(enKey, en.Description)
			values := make([]enumx.Value, len(en.Values))
			for k, v := range en.Values {
				v.Description = tr(enKey+"."+v.Value, v.Description)
				values[k] = v
			}
			en.Values = values
			enums[j] = en
		}
		e.Enums = enums
		out[i] = e
	}
	return out
}
