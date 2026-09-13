package platform

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// App is the application container: logger, metrics, health checks and the values
// modules share with each other.
//
// Modules find each other through the container instead of through edits in main.go,
// which is why adding or removing a module needs no manual changes in project code.
type App struct {
	service string
	role    string
	log     *slog.Logger
	metrics *prometheus.Registry

	mu     sync.RWMutex
	values map[reflect.Type]any
	checks []healthCheck
}

type healthCheck struct {
	name  string
	check func(context.Context) error
}

// NewApp creates a container without running modules. Tests and platform tooling use
// it when the full lifecycle is not needed. The logger may be nil.
func NewApp(log *slog.Logger) *App {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return newApp("app", log)
}

func newApp(service string, log *slog.Logger) *App {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	return &App{
		service: service,
		role:    RoleAll,
		log:     log,
		metrics: reg,
		values:  make(map[reflect.Type]any),
	}
}

// Service returns the service name. Modules put it in page titles and messages.
func (a *App) Service() string { return a.service }

// Role returns what this process does: all, api or worker.
func (a *App) Role() string { return a.role }

// Serves reports whether this process takes the given role: a process in role all
// takes every role. A module that serves requests checks api, one that works jobs
// checks worker.
func (a *App) Serves(role string) bool { return a.role == RoleAll || a.role == role }

// Logger returns the application logger.
func (a *App) Logger() *slog.Logger { return a.log }

// Metrics returns the registry served at /metrics.
func (a *App) Metrics() *prometheus.Registry { return a.metrics }

// AddHealthCheck registers a check for /health. Modules need not call it: the platform
// adds a check for every module implementing HealthChecker.
func (a *App) AddHealthCheck(name string, check func(context.Context) error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.checks = append(a.checks, healthCheck{name: name, check: check})
}

func (a *App) healthChecks() []healthCheck {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]healthCheck(nil), a.checks...)
}

// Provide stores a value in the container under its own type, replacing any previous one.
func Provide[T any](a *App, v T) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.values[reflect.TypeFor[T]()] = v
}

// Lookup returns the value of type T and reports whether it is present.
func Lookup[T any](a *App) (T, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	v, ok := a.values[reflect.TypeFor[T]()]
	if !ok {
		var zero T
		return zero, false
	}
	t, ok := v.(T)
	return t, ok
}

// Get returns the value of type T and panics when it is missing.
//
// Panicking is right here: a missing dependency is a wiring mistake, and it shows up
// on the first run rather than in production under load.
func Get[T any](a *App) T {
	v, ok := Lookup[T](a)
	if !ok {
		panic(fmt.Sprintf("platform: no %s in the container: the module providing it is not enabled", reflect.TypeFor[T]()))
	}
	return v
}
