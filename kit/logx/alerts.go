package logx

import (
	"context"
	"log/slog"
)

// AlertKey is the attribute that raises an alert from an ordinary log line:
//
//	log.ErrorContext(ctx, "the pool is empty", logx.AlertKey, "pool_empty")
//
// Its value is the alert kind. The platform counts such lines in app_alerts_total{kind},
// so a use case needs no metric of its own to page someone.
const AlertKey = "alert"

// Alerts wraps a handler: count receives the kind of every record that carries AlertKey,
// either in the record itself or in attributes added with With. Attributes inside a group
// are not top level and do not count. The record is passed on unchanged.
//
// An alert is counted whatever the log level: a service running with LOG_LEVEL=error
// still counts a warning that raised one, though the line itself is not written. Debug
// lines are the exception, so that disabled debug logging stays free.
func Alerts(next slog.Handler, count func(kind string)) slog.Handler {
	return alertHandler{next: next, count: count}
}

type alertHandler struct {
	next    slog.Handler
	count   func(kind string)
	kind    string // from With, before any group
	grouped bool   // a group is open: later attributes are nested
}

func (h alertHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo || h.next.Enabled(ctx, level)
}

func (h alertHandler) Handle(ctx context.Context, r slog.Record) error {
	kind := h.kind
	if !h.grouped {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == AlertKey {
				kind = a.Value.String()
				return false
			}
			return true
		})
	}
	if kind != "" {
		h.count(kind)
	}
	if !h.next.Enabled(ctx, r.Level) {
		return nil
	}
	return h.next.Handle(ctx, r)
}

func (h alertHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if !h.grouped {
		for _, a := range attrs {
			if a.Key == AlertKey {
				h.kind = a.Value.String()
			}
		}
	}
	h.next = h.next.WithAttrs(attrs)
	return h
}

func (h alertHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	h.next = h.next.WithGroup(name)
	h.grouped = true
	return h
}
