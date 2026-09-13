package logx_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/aidarbn/platform-go/kit/logx"
)

func TestErrorPrintedAsText(t *testing.T) {
	var buf bytes.Buffer
	log := logx.New(logx.Options{Writer: &buf})

	log.Error("failed", "err", fmt.Errorf("wrapper: %w", errors.New("root")))

	out := buf.String()
	if !strings.Contains(out, `"err":"wrapper: root"`) {
		t.Errorf("errors must be rendered as text, got: %s", out)
	}
}

func TestLevelFiltersDebug(t *testing.T) {
	var buf bytes.Buffer
	log := logx.New(logx.Options{Writer: &buf})

	log.Debug("must not be logged")
	if buf.Len() != 0 {
		t.Errorf("default level is info, got: %s", buf.String())
	}
}

func TestTextFormat(t *testing.T) {
	var buf bytes.Buffer
	log := logx.New(logx.Options{Format: "text", Level: "debug", Writer: &buf})

	log.Debug("hello", "key", 1)
	if out := buf.String(); !strings.Contains(out, "msg=hello") || !strings.Contains(out, "key=1") {
		t.Errorf("text format: %s", out)
	}
}

func TestLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug, "info": slog.LevelInfo, "WARN": slog.LevelWarn,
		"warning": slog.LevelWarn, "error": slog.LevelError, "": slog.LevelInfo, "nonsense": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := logx.Level(in); got != want {
			t.Errorf("Level(%q) = %v, want %v", in, got, want)
		}
	}
}

// A log line written with a context of a span carries its ids, so it leads to the trace.
func TestTraceIDsInLogs(t *testing.T) {
	var buf bytes.Buffer
	log := logx.New(logx.Options{Writer: &buf}).With("service", "x")

	tid, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	sid, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled}))

	log.InfoContext(ctx, "with a span")
	log.InfoContext(context.Background(), "without")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if !strings.Contains(lines[0], `"trace_id":"4bf92f3577b34da6a3ce929d0e0e4736"`) || !strings.Contains(lines[0], `"span_id":"00f067aa0ba902b7"`) || !strings.Contains(lines[0], `"service":"x"`) {
		t.Errorf("line with a span: %s", lines[0])
	}
	if strings.Contains(lines[1], "trace_id") {
		t.Errorf("line without a span: %s", lines[1])
	}
}
