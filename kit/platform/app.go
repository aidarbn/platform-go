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

// App — контейнер приложения: логгер, метрики, проверки health и значения,
// которыми модули делятся между собой.
//
// Модули находят друг друга через контейнер, а не через правки в main.go: поэтому
// добавление и удаление модуля не требует ручных изменений в коде проекта.
type App struct {
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

func newApp(log *slog.Logger) *App {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	return &App{
		log:     log,
		metrics: reg,
		values:  make(map[reflect.Type]any),
	}
}

// Logger возвращает логгер приложения.
func (a *App) Logger() *slog.Logger { return a.log }

// Metrics возвращает реестр метрик, который отдаётся в /metrics.
func (a *App) Metrics() *prometheus.Registry { return a.metrics }

// AddHealthCheck добавляет проверку в /health. Модулям это делать не нужно:
// платформа сама добавляет проверку каждого модуля, который реализует HealthChecker.
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

// Provide кладёт значение в контейнер под его типом. Повторный вызов заменяет значение.
func Provide[T any](a *App, v T) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.values[reflect.TypeFor[T]()] = v
}

// Lookup достаёт значение типа T и сообщает, есть ли оно.
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

// Get достаёт значение типа T и паникует, если его нет.
//
// Паника здесь уместна: отсутствие зависимости — ошибка сборки приложения,
// она видна на первом же запуске, а не в проде под нагрузкой.
func Get[T any](a *App) T {
	v, ok := Lookup[T](a)
	if !ok {
		panic(fmt.Sprintf("platform: в контейнере нет %s — не подключён нужный модуль", reflect.TypeFor[T]()))
	}
	return v
}
