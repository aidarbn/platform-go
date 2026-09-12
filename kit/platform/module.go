package platform

import "context"

// Module — инфраструктурная часть приложения: база, очереди, API, админка.
//
// Обязательны только Name и Init. Остальное модуль реализует по необходимости:
// платформа проверяет реализацию Starter, Stopper и HealthChecker и вызывает их,
// если они есть. Так модуль без фоновой работы остаётся в несколько строк.
type Module interface {
	// Name — имя модуля в логах, health и ошибках.
	Name() string

	// Init готовит модуль и кладёт в контейнер то, чем модуль делится с остальными.
	// Порядок вызова — порядок списка модулей.
	Init(ctx context.Context, app *App) error
}

// Starter запускает фоновую работу: воркеры очередей, серверы, подписки.
// Вызывается после сборки домена, чтобы к старту все обработчики были на месте.
type Starter interface {
	Start(ctx context.Context) error
}

// Stopper освобождает ресурсы. Вызывается в порядке, обратном инициализации,
// с общим таймаутом остановки.
type Stopper interface {
	Stop(ctx context.Context) error
}

// HealthChecker добавляет проверку модуля в /health.
type HealthChecker interface {
	Health(ctx context.Context) error
}
