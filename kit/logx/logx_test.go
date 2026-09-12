package logx_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

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
