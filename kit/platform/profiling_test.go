package platform

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grafana/pyroscope-go"
)

func TestTelemetryName(t *testing.T) {
	t.Setenv("OTEL_SERVICE_NAME", "")
	if got := telemetryName("shop"); got != "shop" {
		t.Errorf("without OTEL_SERVICE_NAME: %s", got)
	}
	t.Setenv("OTEL_SERVICE_NAME", " be-maas-api ")
	if got := telemetryName("shop"); got != "be-maas-api" {
		t.Errorf("with OTEL_SERVICE_NAME: %s", got)
	}
}

func TestProfilingOffWithoutAddress(t *testing.T) {
	t.Setenv("PYROSCOPE_ADDR", "")
	called := false
	stubPyroscope(t, func(pyroscope.Config) (profiler, error) { called = true; return nil, nil })
	stop := startProfiling("shop", slog.New(slog.DiscardHandler))
	if called {
		t.Error("the profiler started without PYROSCOPE_ADDR")
	}
	if err := stop(context.Background()); err != nil {
		t.Error(err)
	}
}

// With PYROSCOPE_ADDR the real profiler sends profiles under the service name and
// uploads what is left when it stops.
func TestProfilingSendsProfiles(t *testing.T) {
	var mu sync.Mutex
	var apps []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		apps = append(apps, r.URL.Query().Get("name"))
		mu.Unlock()
	}))
	defer srv.Close()
	t.Setenv("PYROSCOPE_ADDR", srv.URL)

	var logs bytes.Buffer
	stop := startProfiling("be-maas-api", slog.New(slog.NewTextHandler(&logs, nil)))
	time.Sleep(50 * time.Millisecond)
	if err := stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(apps) == 0 || !strings.HasPrefix(apps[0], "be-maas-api{") {
		t.Errorf("uploads: %v", apps)
	}
	if !strings.Contains(logs.String(), "profiling to pyroscope") {
		t.Errorf("no start line:\n%s", logs.String())
	}
}

// A profiler that does not start is logged; the service runs on without it.
func TestProfilingStartFailureIsLogged(t *testing.T) {
	t.Setenv("PYROSCOPE_ADDR", "http://127.0.0.1:1")
	var got pyroscope.Config
	stubPyroscope(t, func(cfg pyroscope.Config) (profiler, error) {
		got = cfg
		return nil, errors.New("cpu profiling already in use")
	})
	var logs bytes.Buffer
	stop := startProfiling("shop", slog.New(slog.NewTextHandler(&logs, nil)))
	if err := stop(context.Background()); err != nil {
		t.Error(err)
	}
	if got.ApplicationName != "shop" || got.ServerAddress != "http://127.0.0.1:1" || len(got.ProfileTypes) != 10 {
		t.Errorf("config: %+v", got)
	}
	if !strings.Contains(logs.String(), "pyroscope did not start") {
		t.Errorf("the failure is not logged:\n%s", logs.String())
	}
}

type stuckProfiler struct{ release chan struct{} }

func (p stuckProfiler) Stop() error { <-p.release; return nil }

// A profiler that cannot finish its upload does not hold the shutdown past its deadline.
func TestProfilingStopHonoursDeadline(t *testing.T) {
	t.Setenv("PYROSCOPE_ADDR", "http://127.0.0.1:1")
	p := stuckProfiler{release: make(chan struct{})}
	defer close(p.release)
	stubPyroscope(t, func(pyroscope.Config) (profiler, error) { return p, nil })
	stop := startProfiling("shop", slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("stop = %v", err)
	}
}

func TestPyroscopeLogger(t *testing.T) {
	var logs bytes.Buffer
	l := pyroscopeLogger{slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	l.Infof("started %d", 1)
	l.Debugf("tick %s", "a")
	l.Errorf("upload failed: %v", "refused")
	for _, want := range []string{"level=DEBUG msg=\"pyroscope: started 1\"", "pyroscope: tick a", "level=WARN msg=\"pyroscope: upload failed: refused\""} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %s:\n%s", want, logs.String())
		}
	}
}

func stubPyroscope(t *testing.T, fn func(pyroscope.Config) (profiler, error)) {
	t.Helper()
	prev := pyroscopeStart
	pyroscopeStart = fn
	t.Cleanup(func() { pyroscopeStart = prev })
}
