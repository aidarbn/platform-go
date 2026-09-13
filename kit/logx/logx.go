// Package logx builds an slog logger configured the same way in every project.
package logx

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// Options configures the logger. Empty fields fall back to defaults:
// info level, json format, stdout.
type Options struct {
	Level  string // debug, info, warn, error
	Format string // json, text
	Writer io.Writer
}

// New creates a logger.
func New(o Options) *slog.Logger {
	w := o.Writer
	if w == nil {
		w = os.Stdout
	}
	ho := &slog.HandlerOptions{Level: Level(o.Level), ReplaceAttr: replaceAttr}

	var h slog.Handler = slog.NewJSONHandler(w, ho)
	if strings.EqualFold(strings.TrimSpace(o.Format), "text") {
		h = slog.NewTextHandler(w, ho)
	}
	return slog.New(traceHandler{h})
}

// traceHandler adds the trace and span ids of the context to a log line, so a line in
// Loki leads to its trace in Tempo. It only works for the *Context logging calls.
type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}

// Level parses a log level. Anything unknown becomes info.
func Level(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// replaceAttr renders errors as text. Without it the JSON handler writes an empty
// object and the log line carries nothing useful.
func replaceAttr(_ []string, a slog.Attr) slog.Attr {
	if err, ok := a.Value.Any().(error); ok {
		if err == nil {
			return slog.String(a.Key, "")
		}
		return slog.String(a.Key, err.Error())
	}
	return a
}
