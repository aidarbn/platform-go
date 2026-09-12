// Package logx builds an slog logger configured the same way in every project.
package logx

import (
	"io"
	"log/slog"
	"os"
	"strings"
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
	return slog.New(h)
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
