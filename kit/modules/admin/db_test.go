package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aidarbn/platform-go/kit/adminx"
	"github.com/aidarbn/platform-go/kit/logx"
	"github.com/aidarbn/platform-go/kit/modules/admin"
	"github.com/aidarbn/platform-go/kit/modules/postgres"
	"github.com/aidarbn/platform-go/kit/modules/settings"
	"github.com/aidarbn/platform-go/kit/pgdb"
	"github.com/aidarbn/platform-go/kit/platform"
	"github.com/aidarbn/platform-go/kit/settingsx"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_TEST_URL")
	if dsn == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}
	pool, err := pgdb.Open(context.Background(), pgdb.Config{URL: dsn})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := admin.EnsureSchema(context.Background(), pool); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	// Running it twice must be harmless: every start of the service does it.
	if err := admin.EnsureSchema(context.Background(), pool); err != nil {
		t.Fatalf("second EnsureSchema: %v", err)
	}
	return pool
}

// The PostgreSQL repositories must behave exactly like the in memory ones the HTTP
// tests run on.
func TestRepositoriesWithRealDatabase(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	users, sessions, audit := admin.NewUserRepo(pool), admin.NewSessionRepo(pool), admin.NewAuditRepo(pool)

	email := fmt.Sprintf("repo-%d@Example.com", time.Now().UnixNano())
	created, err := users.Create(ctx, adminx.User{Email: email, PasswordHash: "hash", Roles: []string{"support"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = users.Delete(context.Background(), created.ID) })

	if created.ID == 0 || created.Email != strings.ToLower(email) || !created.LastLoginAt.IsZero() {
		t.Errorf("created = %+v", created)
	}
	if _, err := users.Create(ctx, adminx.User{Email: email, PasswordHash: "hash"}); err == nil {
		t.Error("a duplicate address was accepted")
	}

	got, err := users.ByEmail(ctx, strings.ToUpper(email))
	if err != nil || got.ID != created.ID || len(got.Roles) != 1 {
		t.Fatalf("ByEmail = %+v, %v", got, err)
	}

	got.Roles = []string{"support", "billing"}
	got.Disabled = true
	got.TOTPSecret, got.TOTPCounter = "SECRET", 42
	got.LastLoginAt = time.Now().UTC().Truncate(time.Microsecond)
	if err := users.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	again, err := users.ByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("ByID: %v", err)
	}
	if !again.Disabled || again.TOTPCounter != 42 || len(again.Roles) != 2 || !again.LastLoginAt.Equal(got.LastLoginAt) {
		t.Errorf("after Update = %+v", again)
	}

	if _, err := users.ByID(ctx, -1); !errors.Is(err, adminx.ErrNoUser) {
		t.Errorf("ByID unknown: %v", err)
	}
	if err := users.Update(ctx, adminx.User{ID: -1, Email: "x@example.com"}); !errors.Is(err, adminx.ErrNoUser) {
		t.Errorf("Update unknown: %v", err)
	}

	list, err := users.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, u := range list {
		found = found || u.ID == created.ID
	}
	if !found {
		t.Error("List lacks the account")
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	live := adminx.Session{ID: "live-" + email, UserID: created.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour), IP: "10.0.0.1", UserAgent: "test"}
	stale := adminx.Session{ID: "stale-" + email, UserID: created.ID, CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)}
	for _, s := range []adminx.Session{live, stale} {
		if err := sessions.Create(ctx, s); err != nil {
			t.Fatalf("session Create: %v", err)
		}
	}
	s, err := sessions.ByID(ctx, live.ID)
	if err != nil || s.UserID != created.ID || s.IP != "10.0.0.1" || !s.ExpiresAt.Equal(live.ExpiresAt) {
		t.Fatalf("session ByID = %+v, %v", s, err)
	}
	if n, err := sessions.DeleteExpired(ctx, now); err != nil || n < 1 {
		t.Errorf("DeleteExpired = %d, %v", n, err)
	}
	if _, err := sessions.ByID(ctx, stale.ID); !errors.Is(err, adminx.ErrNoSession) {
		t.Errorf("the expired session stayed: %v", err)
	}
	if err := sessions.Delete(ctx, "no-such-session"); !errors.Is(err, adminx.ErrNoSession) {
		t.Errorf("Delete unknown: %v", err)
	}

	// Deleting an account takes its sessions with it.
	if err := users.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := sessions.ByID(ctx, live.ID); !errors.Is(err, adminx.ErrNoSession) {
		t.Errorf("the session outlived its account: %v", err)
	}
	if err := users.Delete(ctx, created.ID); !errors.Is(err, adminx.ErrNoUser) {
		t.Errorf("second Delete: %v", err)
	}

	actor := "audit-" + email
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM platform_admin_audit WHERE actor = $1", actor)
	})
	for i := range 3 {
		if err := audit.Add(ctx, adminx.AuditEntry{Actor: actor, Action: fmt.Sprintf("step.%d", i), Target: "t"}); err != nil {
			t.Fatalf("audit Add: %v", err)
		}
	}
	entries, err := audit.List(ctx, 2, 0)
	if err != nil || len(entries) != 2 || entries[0].ID <= entries[1].ID {
		t.Fatalf("audit List = %+v, %v", entries, err)
	}
	older, err := audit.List(ctx, 100, entries[1].ID)
	if err != nil {
		t.Fatalf("audit List older: %v", err)
	}
	for _, e := range older {
		if e.ID >= entries[1].ID {
			t.Errorf("an entry newer than the cursor: %+v", e)
		}
	}
}

// The whole module in the platform lifecycle: the first account is created from the
// environment, it signs in over HTTP, and /health reports the panel.
func TestLifecycleWithRealDatabase(t *testing.T) {
	pool := testPool(t)
	dsn := os.Getenv("DATABASE_TEST_URL")

	email := fmt.Sprintf("boot-%d@example.com", time.Now().UnixNano())
	var existing int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM platform_admin_users").Scan(&existing); err != nil {
		t.Fatal(err)
	}
	if existing > 0 {
		t.Skip("platform_admin_users is not empty: the bootstrap account is only created on an empty table")
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM platform_admin_users WHERE email = $1", email)
		_, _ = pool.Exec(context.Background(), "DELETE FROM platform_admin_audit WHERE actor = $1", email)
	})

	panel := admin.New(admin.Config{
		Addr: "127.0.0.1:0", Insecure: true,
		BootstrapEmail: email, BootstrapPassword: "first-password",
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opsCh, errCh := make(chan string, 1), make(chan error, 1)
	cfg := platform.Config{
		Service: "shop-api", OpsAddr: "127.0.0.1:0", ShutdownTimeout: 5 * time.Second,
		Logger:    logx.New(logx.Options{Writer: io.Discard}),
		OnStarted: func(addr string) { opsCh <- addr },
	}
	go func() {
		// The panel comes before settings, the way generation orders them: the settings
		// pages must still appear.
		schema := settingsx.MustSchema(settingsx.Definition{
			Key: "app.maintenance", Group: "app", Name: "maintenance", Kind: settingsx.KindBool, Default: "false",
		})
		modules := []platform.Module{
			postgres.New(postgres.Config{URL: dsn}),
			panel,
			settings.New(settings.Config{Table: "platform_settings_admin_test"}, schema),
		}
		errCh <- platform.RunContext(ctx, cfg, modules, nil)
	}()

	var ops string
	select {
	case ops = <-opsCh:
	case err := <-errCh:
		t.Fatalf("Run returned before startup: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("the application did not start")
	}

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	resp, err := client.PostForm("http://"+panel.Addr()+"/login", url.Values{"email": {email}, "password": {"first-password"}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "shop-api") || !strings.Contains(string(body), "Overview") {
		t.Fatalf("login: %d\n%s", resp.StatusCode, body)
	}

	page, err := client.Get("http://" + panel.Addr() + "/settings")
	if err != nil {
		t.Fatalf("settings page: %v", err)
	}
	pageBody, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if page.StatusCode != http.StatusOK || !strings.Contains(string(pageBody), "maintenance") {
		t.Errorf("the settings page does not show the settings: %d\n%s", page.StatusCode, pageBody)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP TABLE IF EXISTS platform_settings_admin_test")
	})

	health, err := http.Get("http://" + ops + "/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	defer health.Body.Close()
	var h struct {
		Checks map[string]string `json:"checks"`
	}
	if err := json.NewDecoder(health.Body).Decode(&h); err != nil {
		t.Fatal(err)
	}
	if h.Checks["admin"] != "ok" {
		t.Errorf("health = %+v", h)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
}
