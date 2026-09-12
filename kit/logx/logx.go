// Package logx собирает slog-логгер с одинаковыми настройками во всех проектах.
package logx

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// Options настраивает логгер. Пустые поля берут значения по умолчанию:
// уровень info, формат json, вывод в stdout.
type Options struct {
	Level  string // debug, info, warn, error
	Format string // json, text
	Writer io.Writer
}

// New создаёт логгер.
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

// Level разбирает уровень логирования. Неизвестное значение даёт info.
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

// replaceAttr печатает ошибки текстом: иначе обработчик JSON выводит пустой объект
// и в логе не остаётся ничего полезного.
func replaceAttr(_ []string, a slog.Attr) slog.Attr {
	if err, ok := a.Value.Any().(error); ok {
		if err == nil {
			return slog.String(a.Key, "")
		}
		return slog.String(a.Key, err.Error())
	}
	return a
}
