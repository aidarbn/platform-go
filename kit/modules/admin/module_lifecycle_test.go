package admin_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aidarbn/platform-go/kit/adminx"
	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/logx"
	"github.com/aidarbn/platform-go/kit/modules/admin"
	"github.com/aidarbn/platform-go/kit/modules/postgres"
	"github.com/aidarbn/platform-go/kit/pgdb"
	"github.com/aidarbn/platform-go/kit/platform"
)

func TestLoadReadsTheEnvironment(t *testing.T) {
	t.Setenv("ADMIN_ADDR", "0.0.0.0:9000")
	t.Setenv("ADMIN_SESSION_TTL", "2h")
	t.Setenv("ADMIN_INSECURE_COOKIES", "true")
	t.Setenv("ADMIN_BOOTSTRAP_EMAIL", "root@example.com")
	t.Setenv("ADMIN_BOOTSTRAP_PASSWORD", "secret")
	t.Setenv("ADMIN_LANGUAGE", "ru")
	l := confx.New("")
	cfg := admin.Load(l)
	if err := l.Err(); err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "0.0.0.0:9000" || cfg.SessionTTL != 2*time.Hour || !cfg.Insecure ||
		cfg.BootstrapEmail != "root@example.com" || cfg.BootstrapPassword != "secret" || cfg.Language != "ru" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestLoadDefaultsAndBadLanguage(t *testing.T) {
	l := confx.New("")
	cfg := admin.Load(l)
	if err := l.Err(); err != nil || cfg.Addr != admin.DefaultAddr || cfg.Language != "en" || cfg.SessionTTL != adminx.DefaultSessionTTL {
		t.Fatalf("defaults = %+v, %v", cfg, err)
	}

	t.Setenv("ADMIN_LANGUAGE", "fr")
	l = confx.New("")
	cfg = admin.Load(l)
	if err := l.Err(); err == nil || !strings.Contains(err.Error(), `ADMIN_LANGUAGE="fr"`) || cfg.Language != "en" {
		t.Fatalf("fr: %+v, %v", cfg, err)
	}
}

func TestModuleBeforeInit(t *testing.T) {
	m := admin.New(admin.Config{})
	if m.Name() != "admin" || m.Addr() != admin.DefaultAddr {
		t.Fatalf("name %q, addr %q", m.Name(), m.Addr())
	}
	if err := m.Health(context.Background()); err == nil {
		t.Fatal("health before Start must fail")
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop before Start: %v", err)
	}
	if err := m.Init(context.Background(), platform.NewApp(nil)); err == nil || !strings.Contains(err.Error(), "postgres module") {
		t.Fatalf("Init without a database: %v", err)
	}
}

// adminDatabase is a database of its own: the bootstrap account is only made on an
// empty users table, which the shared test database cannot promise.
func adminDatabase(t *testing.T) (string, *pgxpool.Pool) {
	t.Helper()
	base := os.Getenv("DATABASE_TEST_URL")
	if base == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}
	ctx := context.Background()
	adm, err := pgdb.Open(ctx, pgdb.Config{URL: base})
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("admin_module_%d", time.Now().UnixNano())
	if _, err := adm.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		adm.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = adm.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		adm.Close()
	})
	u, _ := url.Parse(base)
	u.Path = "/" + name
	pool, err := pgdb.Open(ctx, pgdb.Config{URL: u.String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return u.String(), pool
}

func appWithPool(pool *pgxpool.Pool) *platform.App {
	app := platform.NewApp(nil)
	platform.Provide(app, pool)
	return app
}

// Pages and the theme are set in wireDomain; once the panel serves, the registry is
// sealed and a late registration is a bug that panics.
func TestRegistryIsSealedByStart(t *testing.T) {
	_, pool := adminDatabase(t)
	ctx := context.Background()
	app := appWithPool(pool)
	m := admin.New(admin.Config{Addr: "127.0.0.1:0", Insecure: true, BootstrapEmail: "root@example.com", BootstrapPassword: "first-password"})
	if err := m.Init(ctx, app); err != nil {
		t.Fatal(err)
	}
	page := admin.Page{Title: "Orders", Path: "/orders", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})}
	admin.AddPage(app, page)
	admin.SetTheme(app, admin.Theme{Brand: "Shop"})
	if got := admin.Pages(app).Pages(); len(got) != 1 || got[0].Path != "/orders" {
		t.Fatalf("pages = %+v", got)
	}
	if admin.Auth(app) == nil {
		t.Fatal("Auth is nil")
	}

	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(ctx)
	if err := m.Health(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}
	resp, err := http.Get("http://" + m.Addr() + "/login")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Shop") {
		t.Fatalf("login page does not carry the theme: %d\n%s", resp.StatusCode, body)
	}

	mustPanic(t, "AddPage after Start", func() { admin.AddPage(app, admin.Page{Path: "/late"}) })
	mustPanic(t, "SetTheme after Start", func() { admin.SetTheme(app, admin.Theme{}) })
	// Stop twice is harmless: the platform may stop a module that failed to start.
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func mustPanic(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: no panic", name)
		}
	}()
	fn()
}

func TestBootstrap(t *testing.T) {
	_, pool := adminDatabase(t)
	ctx := context.Background()

	// Without the variables a fresh installation starts, with a warning and no account.
	if err := admin.New(admin.Config{}).Init(ctx, appWithPool(pool)); err != nil {
		t.Fatal(err)
	}
	if users, err := admin.NewUserRepo(pool).List(ctx); err != nil || len(users) != 0 {
		t.Fatalf("users = %v, %v", users, err)
	}

	// An address that normalises to nothing cannot become the first account.
	err := admin.New(admin.Config{BootstrapEmail: "   ", BootstrapPassword: "first-password"}).Init(ctx, appWithPool(pool))
	if err == nil || !strings.Contains(err.Error(), "create the first account") {
		t.Fatalf("blank email: %v", err)
	}

	// A broken table stops the start instead of hiding the panel.
	if _, err := pool.Exec(ctx, "DROP TABLE platform_admin_users CASCADE"); err != nil {
		t.Fatal(err)
	}
	if err := admin.New(admin.Config{}).Init(ctx, appWithPool(pool)); err == nil {
		t.Fatal("Init over a dropped users table succeeded")
	}
}

func TestStartOnABusyAddress(t *testing.T) {
	_, pool := adminDatabase(t)
	ctx := context.Background()
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()

	m := admin.New(admin.Config{Addr: busy.Addr().String()})
	if err := m.Init(ctx, appWithPool(pool)); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err == nil || !strings.Contains(err.Error(), "admin panel on "+busy.Addr().String()) {
		t.Fatalf("err = %v", err)
	}
}

// A worker process carries the module for its container values but serves no panel.
func TestWorkerDoesNotServeThePanel(t *testing.T) {
	dsn, _ := adminDatabase(t)
	panel := admin.New(admin.Config{Addr: "127.0.0.1:0"})
	ctx, cancel := context.WithCancel(context.Background())
	started, errCh := make(chan string, 1), make(chan error, 1)
	cfg := platform.Config{
		Service: "shop-worker", Role: platform.RoleWorker, OpsAddr: "127.0.0.1:0", ShutdownTimeout: 5 * time.Second,
		Logger: logx.New(logx.Options{Writer: io.Discard}), OnStarted: func(a string) { started <- a },
	}
	go func() {
		errCh <- platform.RunContext(ctx, cfg, []platform.Module{postgres.New(postgres.Config{URL: dsn}), panel}, nil)
	}()
	select {
	case <-started:
	case err := <-errCh:
		t.Fatalf("Run: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("did not start")
	}
	if panel.Addr() != "127.0.0.1:0" {
		t.Errorf("a worker listens on %s", panel.Addr())
	}
	if err := panel.Health(context.Background()); err != nil {
		t.Errorf("health of a worker: %v", err)
	}
	cancel()
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func TestSessionsOfAUserAreRevoked(t *testing.T) {
	_, pool := adminDatabase(t)
	ctx := context.Background()
	if err := admin.EnsureSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	user, err := admin.NewUserRepo(pool).Create(ctx, adminx.User{Email: "a@example.com", PasswordHash: "x", CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	sessions := admin.NewSessionRepo(pool)
	now := time.Now()
	for _, id := range []string{"s1", "s2"} {
		if err := sessions.Create(ctx, adminx.Session{ID: id, UserID: user.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := sessions.DeleteByUser(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"s1", "s2"} {
		if _, err := sessions.ByID(ctx, id); !errors.Is(err, adminx.ErrNoSession) {
			t.Errorf("%s: %v", id, err)
		}
	}

	// The audit list pages by 50 when no limit is given.
	audit := admin.NewAuditRepo(pool)
	if err := audit.Add(ctx, adminx.AuditEntry{Actor: "a@example.com", Action: "login"}); err != nil {
		t.Fatal(err)
	}
	if entries, err := audit.List(ctx, 0, 0); err != nil || len(entries) != 1 {
		t.Fatalf("audit = %v, %v", entries, err)
	}

	// Failures of the database reach the caller.
	pool.Close()
	if _, err := audit.List(ctx, 10, 0); err == nil {
		t.Error("audit List on a closed pool")
	}
	if err := sessions.Delete(ctx, "s1"); err == nil {
		t.Error("session Delete on a closed pool")
	}
	if _, err := sessions.DeleteExpired(ctx, now); err == nil {
		t.Error("DeleteExpired on a closed pool")
	}
	if _, err := admin.NewUserRepo(pool).List(ctx); err == nil {
		t.Error("user List on a closed pool")
	}
	if err := admin.EnsureSchema(ctx, pool); err == nil {
		t.Error("EnsureSchema on a closed pool")
	}
}
