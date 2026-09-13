package platform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/logx"
)

// Config holds startup parameters. Empty fields fall back to defaults.
type Config struct {
	Service         string        // service name in logs
	Role            string        // what this process does: all, api or worker; all by default
	OpsAddr         string        // ops server address, :9090 by default
	ShutdownTimeout time.Duration // overall shutdown timeout, 20s by default
	Logger          *slog.Logger

	// OnStarted is called once every module and the ops server are up. Tests and local
	// runs use it to learn the actual ops address.
	OnStarted func(opsAddr string)
}

// Roles a process can run in. One binary serves them all: an API instance and a worker
// instance differ only in APP_ROLE.
const (
	RoleAll    = "all"
	RoleAPI    = "api"
	RoleWorker = "worker"
)

// setDefaults fills what the code left empty from the environment, then from defaults,
// so existing projects gain these variables without touching main.go. Every bad
// variable is reported at once.
func (c *Config) setDefaults() error {
	l := confx.New("")
	if c.Service == "" {
		c.Service = "app"
	}
	if c.Role == "" {
		c.Role = l.String("APP_ROLE", RoleAll)
	}
	if !slices.Contains([]string{RoleAll, RoleAPI, RoleWorker}, c.Role) {
		l.Fail(fmt.Errorf("APP_ROLE=%q: expected all, api or worker", c.Role))
	}
	if c.OpsAddr == "" {
		c.OpsAddr = l.String("OPS_ADDR", ":9090")
	}
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = l.Duration("SHUTDOWN_TIMEOUT", 20*time.Second)
	}
	if c.Logger == nil {
		c.Logger = logx.New(logx.Options{Level: l.String("LOG_LEVEL", "info"), Format: l.String("LOG_FORMAT", "json")})
	}
	return l.Err()
}

// Wire builds the project domain: use cases, handlers, workers. It runs between Init
// and Start of every module, so queues and servers begin work with handlers in place.
type Wire func(app *App) error

// Run starts the modules and blocks until SIGINT or SIGTERM.
func Run(cfg Config, modules []Module, wire Wire) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return RunContext(ctx, cfg, modules, wire)
}

// RunContext behaves like Run but stops when ctx is done. Tests use it.
func RunContext(ctx context.Context, cfg Config, modules []Module, wire Wire) error {
	if err := cfg.setDefaults(); err != nil {
		return err
	}
	app := newApp(cfg.Service, cfg.Logger.With("service", cfg.Service))
	app.role = cfg.Role

	// shutdown stops whatever is already up; it runs on every exit path.
	var inited []Module
	shutdown := func() error {
		stopCtx, cancel := shutdownContext(ctx, cfg)
		defer cancel()
		return stopModules(stopCtx, app, inited)
	}

	var err error
	inited, err = initModules(ctx, app, modules)
	if err != nil {
		return errors.Join(err, shutdown())
	}

	if wire != nil {
		if err := wire(app); err != nil {
			return errors.Join(fmt.Errorf("wire domain: %w", err), shutdown())
		}
	}

	if err := startModules(ctx, app, modules); err != nil {
		return errors.Join(err, shutdown())
	}

	ops, err := startOps(cfg.OpsAddr, app)
	if err != nil {
		return errors.Join(err, shutdown())
	}
	app.log.Info("service started", "role", cfg.Role, "ops", ops.addr(), "modules", len(modules))
	if cfg.OnStarted != nil {
		cfg.OnStarted(ops.addr())
	}

	<-ctx.Done()
	app.log.Info("shutting down")

	stopCtx, cancel := shutdownContext(ctx, cfg)
	defer cancel()
	return errors.Join(ops.stop(stopCtx), stopModules(stopCtx, app, inited))
}

// shutdownContext gives shutdown its own deadline: the run context is already cancelled,
// yet modules still need time to close connections and finish in-flight work.
func shutdownContext(ctx context.Context, cfg Config) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
}

func initModules(ctx context.Context, app *App, modules []Module) ([]Module, error) {
	inited := make([]Module, 0, len(modules))
	for _, m := range modules {
		if err := m.Init(ctx, app); err != nil {
			return inited, fmt.Errorf("module %s: init: %w", m.Name(), err)
		}
		inited = append(inited, m)

		if hc, ok := m.(HealthChecker); ok {
			app.AddHealthCheck(m.Name(), hc.Health)
		}
		app.log.Info("module initialised", "module", m.Name())
	}
	return inited, nil
}

func startModules(ctx context.Context, app *App, modules []Module) error {
	for _, m := range modules {
		s, ok := m.(Starter)
		if !ok {
			continue
		}
		if err := s.Start(ctx); err != nil {
			return fmt.Errorf("module %s: start: %w", m.Name(), err)
		}
		app.log.Info("module started", "module", m.Name())
	}
	return nil
}

func stopModules(ctx context.Context, app *App, inited []Module) error {
	var errs []error
	for i := len(inited) - 1; i >= 0; i-- {
		m := inited[i]
		s, ok := m.(Stopper)
		if !ok {
			continue
		}
		if err := s.Stop(ctx); err != nil {
			errs = append(errs, fmt.Errorf("module %s: stop: %w", m.Name(), err))
			continue
		}
		app.log.Info("module stopped", "module", m.Name())
	}
	return errors.Join(errs...)
}
