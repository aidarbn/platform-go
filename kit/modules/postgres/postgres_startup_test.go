package postgres_test

import (
	"context"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/modules/postgres"
	"github.com/aidarbn/platform-go/kit/platform"
)

func TestLoadStartupTimeout(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/app")
	l := confx.New("")
	if cfg := postgres.Load(l); cfg.StartupTimeout != 15*time.Second || l.Err() != nil {
		t.Errorf("default StartupTimeout = %s, err = %v", cfg.StartupTimeout, l.Err())
	}

	t.Setenv("DATABASE_STARTUP_TIMEOUT", "0")
	l = confx.New("")
	if cfg := postgres.Load(l); cfg.StartupTimeout != 0 || l.Err() != nil {
		t.Errorf("StartupTimeout = %s, err = %v, want 0", cfg.StartupTimeout, l.Err())
	}

	t.Setenv("DATABASE_STARTUP_TIMEOUT", "-1s")
	l = confx.New("")
	postgres.Load(l)
	if err := l.Err(); err == nil || !strings.Contains(err.Error(), "DATABASE_STARTUP_TIMEOUT") {
		t.Errorf("a negative timeout: err = %v", err)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestInitRetriesUnreachableDatabase(t *testing.T) {
	m := postgres.New(postgres.Config{
		URL:            "postgres://user:pass@" + freeAddr(t) + "/db?sslmode=disable",
		ConnectTimeout: 2 * time.Second,
		StartupTimeout: 700 * time.Millisecond,
	})
	start := time.Now()
	err := m.Init(context.Background(), platform.NewApp(nil))
	took := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "database is unreachable") {
		t.Fatalf("err = %v", err)
	}
	if took < 700*time.Millisecond || took > 4*time.Second {
		t.Errorf("gave up after %s, want about 700ms", took)
	}
}

func TestInitDoesNotRetryBadURL(t *testing.T) {
	for _, u := range []string{"", "postgres://user:pass@127.0.0.1:1/db?sslmode=nonsense"} {
		start := time.Now()
		err := postgres.New(postgres.Config{URL: u, StartupTimeout: time.Minute}).Init(context.Background(), platform.NewApp(nil))
		if err == nil {
			t.Fatalf("%q was accepted", u)
		}
		if took := time.Since(start); took > time.Second {
			t.Errorf("%q was retried for %s: %v", u, took, err)
		}
	}
}

// proxyLater forwards TCP connections from a fresh local address to target, starting
// only after delay: a database that comes up after the instance.
func proxyLater(t *testing.T, target string, delay time.Duration) string {
	t.Helper()
	addr := freeAddr(t)
	var (
		mu sync.Mutex
		ln net.Listener
	)
	go func() {
		time.Sleep(delay)
		l, err := net.Listen("tcp", addr)
		if err != nil {
			t.Errorf("proxy: %v", err)
			return
		}
		mu.Lock()
		ln = l
		mu.Unlock()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				up, err := net.Dial("tcp", target)
				if err != nil {
					return
				}
				defer up.Close()
				go func() { _, _ = io.Copy(up, c) }()
				_, _ = io.Copy(c, up)
			}()
		}
	}()
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		if ln != nil {
			ln.Close()
		}
	})
	return addr
}

func testDatabaseURL(t *testing.T) *url.URL {
	t.Helper()
	raw := os.Getenv("DATABASE_TEST_URL")
	if raw == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestInitWaitsForDatabaseThatComesUp(t *testing.T) {
	u := testDatabaseURL(t)
	u.Host = proxyLater(t, u.Host, 800*time.Millisecond)

	m := postgres.New(postgres.Config{URL: u.String(), StartupTimeout: 10 * time.Second})
	start := time.Now()
	if err := m.Init(context.Background(), platform.NewApp(nil)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	if took := time.Since(start); took < 800*time.Millisecond {
		t.Errorf("Init succeeded after %s, before the database came up", took)
	}
	if err := m.Health(context.Background()); err != nil {
		t.Errorf("Health: %v", err)
	}
}

func TestInitDoesNotRetryRefusal(t *testing.T) {
	base := testDatabaseURL(t)
	wrongPassword := *base
	wrongPassword.User = url.UserPassword(base.User.Username(), "not-the-password")
	noDatabase := *base
	noDatabase.Path = "/platformgo_no_such_database"

	for name, u := range map[string]*url.URL{"wrong password": &wrongPassword, "no database": &noDatabase} {
		start := time.Now()
		err := postgres.New(postgres.Config{URL: u.String(), StartupTimeout: time.Minute}).Init(context.Background(), platform.NewApp(nil))
		if err == nil {
			t.Fatalf("%s: accepted", name)
		}
		if took := time.Since(start); took > 3*time.Second {
			t.Errorf("%s: retried for %s: %v", name, took, err)
		}
	}
}
