// Package admin is a platform module: the admin panel of the project, running inside
// the same binary as the application.
//
// It brings the parts every project needs anyway — sign in, roles, audit log, business
// settings pages — and an extension point for the project's own pages.
package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aidarbn/platform-go/kit/adminx"
	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/platform"
	"github.com/aidarbn/platform-go/kit/settingsx"
)

// DefaultAddr is the address of the admin panel. It is a separate port so the panel can
// be kept off the public network.
const DefaultAddr = "127.0.0.1:8081"

const (
	sessionCookie     = "platform_admin_session"
	readHeaderTimeout = 5 * time.Second
	cleanupInterval   = time.Hour
)

// Config holds module settings. Load fills it from the environment; the platform
// generator writes the Load call into the project's config.gen.go.
type Config struct {
	Addr       string        // listen address
	SessionTTL time.Duration // how long a session lives

	// Insecure allows the session cookie over plain HTTP. It is meant for local
	// development: in production the panel is behind TLS and the cookie is Secure.
	Insecure bool

	// BootstrapEmail and BootstrapPassword create the first account when the table is
	// empty. Without them a fresh installation has nobody to log in as.
	BootstrapEmail    string
	BootstrapPassword string
}

// Load reads the module settings from environment variables.
func Load(l *confx.Loader) Config {
	return Config{
		Addr:              l.String("ADMIN_ADDR", DefaultAddr),
		SessionTTL:        l.Duration("ADMIN_SESSION_TTL", adminx.DefaultSessionTTL),
		Insecure:          l.Bool("ADMIN_INSECURE_COOKIES", false),
		BootstrapEmail:    l.String("ADMIN_BOOTSTRAP_EMAIL", ""),
		BootstrapPassword: l.String("ADMIN_BOOTSTRAP_PASSWORD", ""),
	}
}

// Page is a project page in the admin panel. It is the extension point: the platform
// gives the layout, the sign in and the roles, the project gives the content.
type Page struct {
	Title   string       // name in the menu
	Path    string       // path inside the panel, "/orders"
	Roles   []string     // who may open it; empty means any signed in user
	Handler http.Handler // the page itself
}

// Registry holds the project pages. It lives in the container, and AddPage fills it
// from wireDomain, before the modules start.
type Registry struct {
	mu     sync.RWMutex
	pages  []Page
	sealed bool
}

// Add registers a page.
func (r *Registry) Add(p Page) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.sealed {
		panic("admin: add pages in wireDomain, before the modules start")
	}
	if err := validatePagePath(p.Path); err != nil {
		panic(err.Error())
	}
	r.pages = append(r.pages, p)
}

// Pages returns the registered pages.
func (r *Registry) Pages() []Page {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.pages)
}

func (r *Registry) seal() []Page {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sealed = true
	return slices.Clone(r.pages)
}

// AddPage registers a project page in the admin panel.
func AddPage(app *platform.App, p Page) { platform.Get[*Registry](app).Add(p) }

// Module implements platform.Module.
type Module struct {
	cfg     Config
	service string
	log     *slog.Logger

	auth     *adminx.Auth
	users    adminx.UserRepo
	audit    adminx.AuditRepo
	settings *settingsx.Store // nil when the settings module is off
	registry *Registry

	srv *http.Server
	ln  net.Listener

	done chan struct{}
	wg   sync.WaitGroup
}

// New creates the module from ready settings.
func New(cfg Config) *Module {
	if cfg.Addr == "" {
		cfg.Addr = DefaultAddr
	}
	return &Module{cfg: cfg, registry: &Registry{}, done: make(chan struct{})}
}

func (m *Module) Name() string { return "admin" }

// Init prepares the storage and the sign in, and puts the page registry into the
// container so that wireDomain can add the project pages.
func (m *Module) Init(ctx context.Context, app *platform.App) error {
	pool, ok := platform.Lookup[*pgxpool.Pool](app)
	if !ok {
		return errors.New("no database in the container: enable the postgres module")
	}
	if err := EnsureSchema(ctx, pool); err != nil {
		return err
	}

	m.log = app.Logger().With("module", "admin")
	m.service = app.Service()
	m.users = NewUserRepo(pool)
	m.audit = NewAuditRepo(pool)
	m.auth = adminx.NewAuth(m.users, NewSessionRepo(pool), m.cfg.SessionTTL)

	// The settings pages appear when the settings module is enabled; without it the
	// panel still works, just without them.
	if store, ok := platform.Lookup[*settingsx.Store](app); ok {
		m.settings = store
	}

	if err := m.bootstrap(ctx); err != nil {
		return err
	}

	platform.Provide(app, m.registry)
	platform.Provide(app, m.auth)
	return nil
}

// bootstrap creates the first account so that a fresh installation has somebody to log
// in as. It runs only while the table is empty.
func (m *Module) bootstrap(ctx context.Context) error {
	users, err := m.users.List(ctx)
	if err != nil {
		return err
	}
	if len(users) > 0 {
		return nil
	}

	if m.cfg.BootstrapEmail == "" || m.cfg.BootstrapPassword == "" {
		m.log.Warn("the admin panel has no accounts: set ADMIN_BOOTSTRAP_EMAIL and ADMIN_BOOTSTRAP_PASSWORD")
		return nil
	}

	user, _, err := m.auth.CreateUser(ctx, m.cfg.BootstrapEmail, m.cfg.BootstrapPassword, []string{adminx.RoleAdmin}, false)
	if err != nil {
		return fmt.Errorf("admin: create the first account: %w", err)
	}
	m.log.Info("the first admin account is created", "email", user.Email,
		"note", "set up two factor authentication and change the password")
	return nil
}

// Start serves the panel and begins removing expired sessions.
func (m *Module) Start(context.Context) error {
	ln, err := net.Listen("tcp", m.cfg.Addr)
	if err != nil {
		return fmt.Errorf("admin panel on %s: %w", m.cfg.Addr, err)
	}
	m.ln = ln
	handler := NewServer(m.cfg, m.service, m.auth, m.users, m.audit, m.settings, m.registry.seal(), m.log)
	m.srv = &http.Server{Handler: handler, ReadHeaderTimeout: readHeaderTimeout}

	go func() {
		if err := m.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.log.Error("the admin panel stopped with an error", "err", err)
		}
	}()
	m.log.Info("the admin panel is listening", "addr", m.Addr())

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()

		for {
			select {
			case <-m.done:
				return
			case <-ticker.C:
				if n, err := m.auth.Cleanup(context.Background()); err != nil {
					m.log.Error("failed to clean up sessions", "err", err)
				} else if n > 0 {
					m.log.Info("expired sessions removed", "count", n)
				}
			}
		}
	}()
	return nil
}

// Addr is the address the panel actually listens on. With port 0 in the settings it is
// the port the system picked, which is what tests use.
func (m *Module) Addr() string {
	if m.ln == nil {
		return m.cfg.Addr
	}
	return m.ln.Addr().String()
}

// Health reports whether the panel is serving.
func (m *Module) Health(context.Context) error {
	if m.ln == nil {
		return errors.New("the admin panel is not started")
	}
	return nil
}

// Stop shuts the panel down.
func (m *Module) Stop(ctx context.Context) error {
	select {
	case <-m.done:
	default:
		close(m.done)
	}
	m.wg.Wait()

	if m.srv == nil {
		return nil
	}
	return m.srv.Shutdown(ctx)
}

// Auth returns the sign in service from the container: project pages use it to know
// who is signed in.
func Auth(app *platform.App) *adminx.Auth { return platform.Get[*adminx.Auth](app) }

// Pages returns the page registry from the container.
func Pages(app *platform.App) *Registry { return platform.Get[*Registry](app) }
