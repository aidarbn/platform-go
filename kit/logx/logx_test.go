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

	log.Error("не вышло", "err", fmt.Errorf("обёртка: %w", errors.New("корень")))

	out := buf.String()
	if !strings.Contains(out, `"err":"обёртка: корень"`) {
		t.Errorf("ошибка должна печататься текстом, получили: %s", out)
	}
}

func TestLevelFiltersDebug(t *testing.T) {
	var buf bytes.Buffer
	log := logx.New(logx.Options{Writer: &buf})

	log.Debug("не должно попасть в лог")
	if buf.Len() != 0 {
		t.Errorf("по умолчанию уровень info, получили: %s", buf.String())
	}
}

func TestTextFormat(t *testing.T) {
	var buf bytes.Buffer
	log := logx.New(logx.Options{Format: "text", Level: "debug", Writer: &buf})

	log.Debug("привет", "ключ", 1)
	if out := buf.String(); !strings.Contains(out, "msg=привет") || !strings.Contains(out, "ключ=1") {
		t.Errorf("текстовый формат: %s", out)
	}
}

func TestLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug, "info": slog.LevelInfo, "WARN": slog.LevelWarn,
		"warning": slog.LevelWarn, "error": slog.LevelError, "": slog.LevelInfo, "чепуха": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := logx.Level(in); got != want {
			t.Errorf("Level(%q) = %v, ждали %v", in, got, want)
		}
	}
}
