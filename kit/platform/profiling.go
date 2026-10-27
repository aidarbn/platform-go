package platform

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/grafana/pyroscope-go"
)

// profiler is the running profiler; Stop uploads what is left.
type profiler interface{ Stop() error }

// pyroscopeStart starts the profiler; tests replace it.
var pyroscopeStart = func(cfg pyroscope.Config) (profiler, error) { return pyroscope.Start(cfg) }

// profileTypes are taply's: CPU, memory, goroutines, mutexes and blocking.
var profileTypes = []pyroscope.ProfileType{
	pyroscope.ProfileCPU,
	pyroscope.ProfileAllocObjects,
	pyroscope.ProfileAllocSpace,
	pyroscope.ProfileInuseObjects,
	pyroscope.ProfileInuseSpace,
	pyroscope.ProfileGoroutines,
	pyroscope.ProfileMutexCount,
	pyroscope.ProfileMutexDuration,
	pyroscope.ProfileBlockCount,
	pyroscope.ProfileBlockDuration,
}

// startProfiling sends continuous profiles to Pyroscope, as taply does, when
// PYROSCOPE_ADDR is set; without it nothing runs. The application is named like the
// service in the traces. Profiling is not worth failing a start over: a profiler that
// does not start is logged and the service runs without it.
func startProfiling(service string, log *slog.Logger) func(context.Context) error {
	off := func(context.Context) error { return nil }
	addr := strings.TrimSpace(os.Getenv("PYROSCOPE_ADDR"))
	if addr == "" {
		return off
	}
	p, err := pyroscopeStart(pyroscope.Config{
		ApplicationName: service,
		ServerAddress:   addr,
		Logger:          pyroscopeLogger{log},
		ProfileTypes:    profileTypes,
	})
	if err != nil {
		log.Error("profiling is off: pyroscope did not start", "addr", addr, "err", err)
		return off
	}
	log.Info("profiling to pyroscope", "addr", addr, "application", service)
	// Stop uploads what is left; a profiler that cannot reach the server must not hold
	// the shutdown past its deadline.
	return func(ctx context.Context) error {
		done := make(chan error, 1)
		go func() { done <- p.Stop() }()
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return fmt.Errorf("profiling: %w", ctx.Err())
		}
	}
}

// pyroscopeLogger passes the profiler's errors, such as a failed upload, to the
// application log; its progress messages are debug lines.
type pyroscopeLogger struct{ log *slog.Logger }

func (l pyroscopeLogger) Infof(format string, args ...any) {
	l.log.Debug("pyroscope: " + fmt.Sprintf(format, args...))
}

func (l pyroscopeLogger) Debugf(format string, args ...any) {
	l.log.Debug("pyroscope: " + fmt.Sprintf(format, args...))
}

func (l pyroscopeLogger) Errorf(format string, args ...any) {
	l.log.Warn("pyroscope: " + fmt.Sprintf(format, args...))
}
