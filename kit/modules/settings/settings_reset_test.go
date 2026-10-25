package settings_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aidarbn/platform-go/kit/modules/postgres"
	"github.com/aidarbn/platform-go/kit/modules/settings"
	"github.com/aidarbn/platform-go/kit/pgdb"
	"github.com/aidarbn/platform-go/kit/platform"
)

// settingsDatabase is a database of its own, so the table and its version start fresh.
func settingsDatabase(t *testing.T) string {
	t.Helper()
	base := os.Getenv("DATABASE_TEST_URL")
	if base == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgdb.Open(ctx, pgdb.Config{URL: base})
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("settings_module_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		admin.Close()
	})
	u, _ := url.Parse(base)
	u.Path = "/" + name
	return u.String()
}

// Reset removes the override from the table: a fresh store reads the default again.
func TestResetRemovesTheOverride(t *testing.T) {
	dsn := settingsDatabase(t)
	ctx := context.Background()
	app := platform.NewApp(nil)
	pg := postgres.New(postgres.Config{URL: dsn})
	if err := pg.Init(ctx, app); err != nil {
		t.Fatal(err)
	}
	defer pg.Stop(ctx)
	if err := settings.New(settings.Config{}, schema(t)).Init(ctx, app); err != nil {
		t.Fatal(err)
	}
	store := settings.Store(app)
	if err := store.Set(ctx, "api.ratelimit.rps", "120", "test"); err != nil {
		t.Fatal(err)
	}
	if err := store.Reset(ctx, "api.ratelimit.rps", "test"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := postgres.Pool(app).QueryRow(ctx, "SELECT count(*) FROM "+settings.DefaultTable).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows left = %d, %v", n, err)
	}
	if got := store.Int("api.ratelimit.rps"); got != 50 {
		t.Fatalf("rps after Reset = %d", got)
	}

	// A database that refuses the table stops the start.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	err := settings.New(settings.Config{Table: "platform_settings_other"}, schema(t)).Init(cancelled, app)
	if err == nil || !strings.HasPrefix(err.Error(), "settings: ") {
		t.Fatalf("cancelled Init: %v", err)
	}
}
