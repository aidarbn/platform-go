package platform_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aidarbn/platform-go/kit/logx"
	"github.com/aidarbn/platform-go/kit/platform"
)

// roleProbe records the role a module sees.
type roleProbe struct{ role chan string }

func (roleProbe) Name() string { return "probe" }

func (p roleProbe) Init(_ context.Context, app *platform.App) error {
	p.role <- app.Role()
	return nil
}

func runBriefly(t *testing.T, cfg platform.Config, modules ...platform.Module) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg.OpsAddr = "127.0.0.1:0"
	started := make(chan string, 1)
	cfg.OnStarted = func(addr string) { started <- addr }
	errCh := make(chan error, 1)
	go func() { errCh <- platform.RunContext(ctx, cfg, modules, nil) }()
	select {
	case <-started:
		cancel()
		return <-errCh
	case err := <-errCh:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the service did not start")
		return nil
	}
}

func TestRoleFromEnvironment(t *testing.T) {
	t.Setenv("APP_ROLE", "worker")
	probe := roleProbe{role: make(chan string, 1)}
	if err := runBriefly(t, platform.Config{Logger: logx.New(logx.Options{Writer: io.Discard})}, probe); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := <-probe.role; got != platform.RoleWorker {
		t.Errorf("role = %q", got)
	}
}

func TestRoleDefaultsToAll(t *testing.T) {
	t.Setenv("APP_ROLE", "")
	probe := roleProbe{role: make(chan string, 1)}
	if err := runBriefly(t, platform.Config{Logger: logx.New(logx.Options{Writer: io.Discard})}, probe); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := <-probe.role; got != platform.RoleAll {
		t.Errorf("role = %q", got)
	}
}

// Bad platform variables stop the start with every problem listed at once.
func TestBadPlatformEnvironment(t *testing.T) {
	t.Setenv("APP_ROLE", "scheduler")
	t.Setenv("SHUTDOWN_TIMEOUT", "soon")
	err := platform.RunContext(context.Background(), platform.Config{Logger: logx.New(logx.Options{Writer: io.Discard})}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "APP_ROLE") || !strings.Contains(err.Error(), "SHUTDOWN_TIMEOUT") {
		t.Fatalf("err = %v", err)
	}
}

func TestServes(t *testing.T) {
	app := platform.NewApp(nil)
	if !app.Serves(platform.RoleAPI) || !app.Serves(platform.RoleWorker) {
		t.Error("a process in role all must take every role")
	}
}
