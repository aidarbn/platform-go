package s3_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/modules/s3"
	"github.com/aidarbn/platform-go/kit/platform"
)

func TestLoadStartupSettings(t *testing.T) {
	t.Setenv("S3_ENDPOINT", "127.0.0.1:9000")
	t.Setenv("S3_ACCESS_KEY", "a")
	t.Setenv("S3_SECRET_KEY", "b")

	l := confx.New("")
	cfg := s3.Load(l)
	if err := l.Err(); err != nil {
		t.Fatal(err)
	}
	if cfg.StartupTimeout != 15*time.Second || cfg.Optional {
		t.Errorf("defaults: StartupTimeout = %s, Optional = %v", cfg.StartupTimeout, cfg.Optional)
	}

	t.Setenv("S3_STARTUP_TIMEOUT", "0")
	t.Setenv("S3_OPTIONAL", "true")
	l = confx.New("")
	cfg = s3.Load(l)
	if err := l.Err(); err != nil || cfg.StartupTimeout != 0 || !cfg.Optional {
		t.Errorf("set: StartupTimeout = %s, Optional = %v, err = %v", cfg.StartupTimeout, cfg.Optional, err)
	}

	t.Setenv("S3_STARTUP_TIMEOUT", "-1s")
	l = confx.New("")
	s3.Load(l)
	if err := l.Err(); err == nil || !strings.Contains(err.Error(), "S3_STARTUP_TIMEOUT") {
		t.Errorf("a negative timeout: err = %v", err)
	}
}

// closedAddr returns a local address nothing listens on: connections are refused.
func closedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestInitRetriesUnreachableStorage(t *testing.T) {
	cfg := s3.Config{Endpoint: closedAddr(t), AccessKey: "a", SecretKey: "b", StartupTimeout: 700 * time.Millisecond}
	start := time.Now()
	err := s3.New(cfg).Init(context.Background(), platform.NewApp(nil))
	took := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "storage is unreachable") {
		t.Fatalf("err = %v, want unreachable", err)
	}
	if took < 700*time.Millisecond || took > 3*time.Second {
		t.Errorf("gave up after %s, want about 700ms", took)
	}

	// Zero: one attempt, as before the retries.
	cfg.StartupTimeout = 0
	start = time.Now()
	if err := s3.New(cfg).Init(context.Background(), platform.NewApp(nil)); err == nil {
		t.Fatal("an unreachable storage was accepted")
	}
	if took := time.Since(start); took > 200*time.Millisecond {
		t.Errorf("a single attempt took %s", took)
	}
}

func TestInitStopsRetryingWhenCancelled(t *testing.T) {
	cfg := s3.Config{Endpoint: closedAddr(t), AccessKey: "a", SecretKey: "b", StartupTimeout: time.Minute}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := s3.New(cfg).Init(ctx, platform.NewApp(nil)); err == nil {
		t.Fatal("an unreachable storage was accepted")
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("a cancelled start retried for %s", took)
	}
}

func TestInitOptionalStartsWithoutStorage(t *testing.T) {
	cfg := s3.Config{Endpoint: closedAddr(t), AccessKey: "a", SecretKey: "b", StartupTimeout: time.Minute, Optional: true}
	app := platform.NewApp(nil)
	m := s3.New(cfg)
	start := time.Now()
	if err := m.Init(context.Background(), app); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("an optional storage held the start for %s", took)
	}

	err := m.Health(context.Background())
	if err == nil || !platform.IsDegraded(err) || !strings.Contains(err.Error(), "storage is unreachable") {
		t.Errorf("Health = %v, want degraded and unreachable", err)
	}

	st := s3.From(app)
	if st == nil {
		t.Fatal("no storage in the container")
	}
	if _, err := st.Put(context.Background(), "files", false, "a.txt", "text/plain", []byte("x")); err == nil {
		t.Error("Put to an unreachable storage succeeded")
	}
	if _, err := st.Get(context.Background(), "files", "a.txt"); err == nil || errors.Is(err, s3.ErrNotFound) {
		t.Errorf("Get = %v, want a storage error", err)
	}
}

func TestInitWaitsForStorageThatComesUp(t *testing.T) {
	cfg := testConfig(t)
	target, err := url.Parse("http://" + cfg.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UseSSL {
		target.Scheme = "https"
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	// The storage comes up on its address 800ms after the instance starts. The proxy
	// keeps the Host header, so the signature MinIO checks is the one made for addr.
	srv := &http.Server{Handler: httputil.NewSingleHostReverseProxy(target), ReadHeaderTimeout: time.Second}
	served := make(chan error, 1)
	go func() {
		time.Sleep(800 * time.Millisecond)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			served <- err
			return
		}
		served <- srv.Serve(ln)
	}()
	t.Cleanup(func() {
		_ = srv.Close()
	})

	cfg.Endpoint, cfg.UseSSL, cfg.StartupTimeout = addr, false, 10*time.Second
	app := platform.NewApp(nil)
	m := s3.New(cfg)
	start := time.Now()
	if err := m.Init(context.Background(), app); err != nil {
		select {
		case serr := <-served:
			t.Fatalf("Init: %v (proxy: %v)", err, serr)
		default:
			t.Fatalf("Init: %v", err)
		}
	}
	if took := time.Since(start); took < 800*time.Millisecond {
		t.Errorf("Init succeeded after %s, before the storage came up", took)
	}
	if err := m.Health(context.Background()); err != nil {
		t.Errorf("Health: %v", err)
	}
}

func TestInitWrongKeysFailAtOnce(t *testing.T) {
	cfg := testConfig(t)
	cfg.SecretKey = "not-the-key"
	cfg.StartupTimeout = time.Minute
	start := time.Now()
	err := s3.New(cfg).Init(context.Background(), platform.NewApp(nil))
	if err == nil {
		t.Fatal("wrong keys were accepted")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("wrong keys were retried for %s: %v", took, err)
	}
}

func TestOptionalHealthyStorage(t *testing.T) {
	cfg := testConfig(t)
	cfg.Optional = true
	m := s3.New(cfg)
	if err := m.Init(context.Background(), platform.NewApp(nil)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := m.Health(context.Background()); err != nil {
		t.Errorf("Health = %v, want nil", err)
	}
}
