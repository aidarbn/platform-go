package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/logx"
	"github.com/aidarbn/platform-go/kit/modules/postgres"
	"github.com/aidarbn/platform-go/kit/platform"
)

func TestLoadRequiresURL(t *testing.T) {
	l := confx.New("")
	postgres.Load(l)

	err := l.Err()
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/app")

	l := confx.New("")
	cfg := postgres.Load(l)

	if err := l.Err(); err != nil {
		t.Fatalf("err = %v", err)
	}
	if cfg.URL != "postgres://localhost/app" || cfg.MaxConns != 10 || cfg.ConnectTimeout != 5*time.Second {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestLoadReadsValues(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://localhost/app")
	t.Setenv("DATABASE_MAX_CONNS", "42")
	t.Setenv("DATABASE_CONNECT_TIMEOUT", "2s")

	l := confx.New("")
	cfg := postgres.Load(l)

	if err := l.Err(); err != nil {
		t.Fatalf("err = %v", err)
	}
	if cfg.MaxConns != 42 || cfg.ConnectTimeout != 2*time.Second {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestInitFailsOnUnreachableDatabase(t *testing.T) {
	m := postgres.New(postgres.Config{
		URL:            "postgres://user:pass@127.0.0.1:1/db?sslmode=disable",
		ConnectTimeout: 2 * time.Second,
	})

	err := m.Init(context.Background(), platform.NewApp(nil))
	if err == nil || !strings.Contains(err.Error(), "база недоступна") {
		t.Fatalf("err = %v", err)
	}
}

func TestHealthWithoutInit(t *testing.T) {
	if err := postgres.New(postgres.Config{}).Health(context.Background()); err == nil {
		t.Fatal("до Init health должен возвращать ошибку")
	}
}

// Полный жизненный цикл на настоящей базе. Запускается, если задан DATABASE_TEST_URL:
// платформа поднимает модуль, health отвечает ok, метрики пула на месте.
func TestLifecycleWithRealDatabase(t *testing.T) {
	url := os.Getenv("DATABASE_TEST_URL")
	if url == "" {
		t.Skip("DATABASE_TEST_URL не задан")
	}

	ctx, cancel := context.WithCancel(context.Background())
	addrCh := make(chan string, 1)
	errCh := make(chan error, 1)

	cfg := platform.Config{
		Service:         "тест",
		OpsAddr:         "127.0.0.1:0",
		ShutdownTimeout: 5 * time.Second,
		Logger:          logx.New(logx.Options{Writer: io.Discard}),
		OnStarted:       func(addr string) { addrCh <- addr },
	}
	modules := []platform.Module{postgres.New(postgres.Config{URL: url})}

	go func() { errCh <- platform.RunContext(ctx, cfg, modules, nil) }()

	var addr string
	select {
	case addr = <-addrCh:
	case err := <-errCh:
		cancel()
		t.Fatalf("Run завершился до запуска: %v", err)
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("приложение не запустилось")
	}

	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	defer resp.Body.Close()

	var body struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("разбор ответа: %v", err)
	}
	if resp.StatusCode != http.StatusOK || body.Checks["postgres"] != "ok" {
		t.Errorf("health = %d %+v", resp.StatusCode, body)
	}

	metrics, err := http.Get("http://" + addr + "/metrics")
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	defer metrics.Body.Close()
	raw, _ := io.ReadAll(metrics.Body)
	if !strings.Contains(string(raw), "pgdb_pool_max_conns") {
		t.Error("метрик пула нет в /metrics")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal(errors.New("Run не завершился"))
	}
}
