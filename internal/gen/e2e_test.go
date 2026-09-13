package gen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/internal/gen"
	"github.com/aidarbn/platform-go/internal/spec"
)

// End to end check: a project is created in an empty directory, the module wiring is
// generated and the whole thing compiles. The project holds only the domain part —
// main.go and the wiring function; everything else is generated.
func TestGeneratedProjectCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("building a project takes time")
	}

	root := repoRoot(t)
	dir := t.TempDir()

	write(t, dir, spec.FileName, `schema: 1
platform: v0.1.0
project:
  module: example.com/app
  service: shop-api
modules:
  postgres: {}
`)

	write(t, dir, "go.mod", `module example.com/app

go 1.27

require github.com/aidarbn/platform-go v0.0.0

replace github.com/aidarbn/platform-go => `+root+`
`)

	// The only hand written files of the project: entry point and domain wiring.
	write(t, dir, "cmd/app/main.go", `package main

import (
	"log"

	"github.com/aidarbn/platform-go/kit/platform"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatal(err)
	}
	if err := platform.Run(platform.Config{Service: "shop-api"}, platformModules(cfg), wireDomain); err != nil {
		log.Fatal(err)
	}
}
`)

	write(t, dir, "cmd/app/wire.go", `package main

import (
	"github.com/aidarbn/platform-go/kit/modules/postgres"
	"github.com/aidarbn/platform-go/kit/platform"
)

// wireDomain is the domain part of the project.
func wireDomain(app *platform.App) error {
	_ = postgres.Pool(app) // project repositories take the pool from the container
	return nil
}
`)

	f, err := spec.Load(filepath.Join(dir, spec.FileName))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	files, err := gen.Wiring(f)
	if err != nil {
		t.Fatalf("Wiring: %v", err)
	}
	if _, err := gen.Apply(dir, files); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	goCmd(t, dir, "mod", "tidy")
	goCmd(t, dir, "build", "./...")
	goCmd(t, dir, "vet", "./...")

	// Generated files must pass gofmt: the project CI checks it.
	if unformatted := gofmtList(t, dir); unformatted != "" {
		t.Errorf("gofmt reported unformatted files:\n%s", unformatted)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the repository path")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func write(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func goCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out := goOutput(t, dir, args...); strings.TrimSpace(out) != "" {
		t.Logf("go %s: %s", strings.Join(args, " "), out)
	}
}

func goOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GOFLAGS=-mod=mod",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// gofmtList returns files that do not pass gofmt.
func gofmtList(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("gofmt", "-l", ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gofmt: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// A project with business settings and an admin panel must compile too: the generated
// accessor is the only way domain code reads a setting, and a project page is the
// extension point of the panel, so the compiler verifies both shapes.
func TestProjectWithSettingsCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("building a project takes time")
	}

	root := repoRoot(t)
	dir := t.TempDir()

	write(t, dir, spec.FileName, `schema: 1
platform: v0.1.0
project:
  module: example.com/app
  service: shop-api
modules:
  postgres: {}
  settings: {}
  admin: {}
  api: {}
  river: {}
  s3: {}
`)

	write(t, dir, "settings.yaml", `settings:
  orders.cleanup:
    enabled:  { type: bool, default: true }
    schedule: { type: cron, default: "0 3 * * *" }
  orders.create:
    max_attempts: { type: int, default: 5, min: 1, max: 20 }
    timeout:      { type: duration, default: 30s }
`)

	write(t, dir, "go.mod", `module example.com/app

go 1.27

require github.com/aidarbn/platform-go v0.0.0

replace github.com/aidarbn/platform-go => `+root+`
`)

	write(t, dir, "cmd/app/main.go", `package main

import (
	"log"

	"github.com/aidarbn/platform-go/kit/platform"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatal(err)
	}
	if err := platform.Run(platform.Config{Service: "shop-api"}, platformModules(cfg), wireDomain); err != nil {
		log.Fatal(err)
	}
}
`)

	// The domain reads business settings as typed method calls, not as string keys.
	write(t, dir, "cmd/app/wire.go", `package main

import (
	"context"
	"net/http"
	"time"

	appsettings "example.com/app/internal/settings"
	"github.com/aidarbn/platform-go/kit/modules/admin"
	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/modules/riverx"
	"github.com/aidarbn/platform-go/kit/modules/s3"
	"github.com/aidarbn/platform-go/kit/platform"
)

func wireDomain(app *platform.App) error {
	s := appsettings.From(app)

	// A project page in the admin panel: the platform gives the layout, the sign in
	// and the roles, the project gives the content.
	// A job queued on every start; workers and periodic jobs are declared the same way.
	riverx.AtStart(app, func(ctx context.Context, q *riverx.Queue) error { return nil })

	// Files in object storage, with taply's storage API.
	_ = s3.From

	// A plain HTTP route next to the gateway.
	api.HandleHTTP(app, "GET /webhooks/ping", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("pong"))
	}))

	admin.AddPage(app, admin.Page{
		Title: "Orders",
		Path:  "/orders",
		Roles: []string{"support"},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, _ := admin.UserFrom(r.Context())
			_, _ = w.Write([]byte(user.Email))
		}),
	})

	var (
		attempts int           = s.OrdersCreate().MaxAttempts()
		timeout  time.Duration = s.OrdersCreate().Timeout()
		schedule string        = s.OrdersCleanup().Schedule()
		enabled  bool          = s.OrdersCleanup().Enabled()
	)
	_, _, _, _ = attempts, timeout, schedule, enabled

	// A schedule that follows the settings takes effect without a restart.
	s.Store().Watch(func(changed []string) { _ = changed })
	return nil
}
`)

	f, err := spec.Load(filepath.Join(dir, spec.FileName))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	files, err := gen.Files(dir, f)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if _, err := gen.Apply(dir, files); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	goCmd(t, dir, "mod", "tidy")
	goCmd(t, dir, "build", "./...")
	goCmd(t, dir, "vet", "./...")

	if unformatted := gofmtList(t, dir); unformatted != "" {
		t.Errorf("gofmt reported unformatted files:\n%s", unformatted)
	}
}
