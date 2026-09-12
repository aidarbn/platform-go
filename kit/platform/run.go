package platform

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aidarbn/platform-go/kit/logx"
)

// Config — параметры запуска приложения. Пустые поля берут значения по умолчанию.
type Config struct {
	Service         string        // имя сервиса в логах
	OpsAddr         string        // адрес служебного сервера, по умолчанию :9090
	ShutdownTimeout time.Duration // общий таймаут остановки, по умолчанию 20s
	Logger          *slog.Logger

	// OnStarted вызывается, когда подняты все модули и служебный сервер.
	// Пригодится тестам и локальному запуску: сообщает фактический адрес.
	OnStarted func(opsAddr string)
}

func (c *Config) setDefaults() {
	if c.Service == "" {
		c.Service = "app"
	}
	if c.OpsAddr == "" {
		c.OpsAddr = ":9090"
	}
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = 20 * time.Second
	}
	if c.Logger == nil {
		c.Logger = logx.New(logx.Options{})
	}
}

// Wire — сборка предметной части проекта: сценарии, обработчики, воркеры.
// Вызывается между Init и Start всех модулей, поэтому к старту очередей и серверов
// все обработчики уже зарегистрированы.
type Wire func(app *App) error

// Run поднимает модули и блокируется до SIGINT или SIGTERM.
func Run(cfg Config, modules []Module, wire Wire) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return RunContext(ctx, cfg, modules, wire)
}

// RunContext — то же, но останавливается по завершении ctx. Используется в тестах.
func RunContext(ctx context.Context, cfg Config, modules []Module, wire Wire) error {
	cfg.setDefaults()
	app := newApp(cfg.Logger.With("service", cfg.Service))

	// shutdown останавливает уже поднятое: вызывается на любом пути выхода.
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
			return errors.Join(fmt.Errorf("сборка домена: %w", err), shutdown())
		}
	}

	if err := startModules(ctx, app, modules); err != nil {
		return errors.Join(err, shutdown())
	}

	ops, err := startOps(cfg.OpsAddr, app)
	if err != nil {
		return errors.Join(err, shutdown())
	}
	app.log.Info("сервис запущен", "ops", ops.addr(), "модулей", len(modules))
	if cfg.OnStarted != nil {
		cfg.OnStarted(ops.addr())
	}

	<-ctx.Done()
	app.log.Info("останавливаемся")

	stopCtx, cancel := shutdownContext(ctx, cfg)
	defer cancel()
	return errors.Join(ops.stop(stopCtx), stopModules(stopCtx, app, inited))
}

// shutdownContext даёт остановке собственный таймаут: контекст запуска уже отменён,
// а модулям нужно время закрыть соединения и дописать задания.
func shutdownContext(ctx context.Context, cfg Config) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
}

func initModules(ctx context.Context, app *App, modules []Module) ([]Module, error) {
	inited := make([]Module, 0, len(modules))
	for _, m := range modules {
		if err := m.Init(ctx, app); err != nil {
			return inited, fmt.Errorf("модуль %s: инициализация: %w", m.Name(), err)
		}
		inited = append(inited, m)

		if hc, ok := m.(HealthChecker); ok {
			app.AddHealthCheck(m.Name(), hc.Health)
		}
		app.log.Info("модуль подготовлен", "модуль", m.Name())
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
			return fmt.Errorf("модуль %s: запуск: %w", m.Name(), err)
		}
		app.log.Info("модуль запущен", "модуль", m.Name())
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
			errs = append(errs, fmt.Errorf("модуль %s: остановка: %w", m.Name(), err))
			continue
		}
		app.log.Info("модуль остановлен", "модуль", m.Name())
	}
	return errors.Join(errs...)
}
