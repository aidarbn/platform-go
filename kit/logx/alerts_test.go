package logx_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/aidarbn/platform-go/kit/logx"
)

func alertLogger() (*slog.Logger, *[]string, *bytes.Buffer) {
	var buf bytes.Buffer
	var kinds []string
	log := slog.New(logx.Alerts(slog.NewJSONHandler(&buf, nil), func(kind string) { kinds = append(kinds, kind) }))
	return log, &kinds, &buf
}

func TestAlertsCountsRecordAttribute(t *testing.T) {
	log, kinds, buf := alertLogger()

	log.Error("pool is empty", logx.AlertKey, "pool_empty")
	log.Info("nothing to see")

	if strings.Join(*kinds, ",") != "pool_empty" {
		t.Errorf("kinds = %v, want [pool_empty]", *kinds)
	}
	if !strings.Contains(buf.String(), `"alert":"pool_empty"`) {
		t.Errorf("the record must be passed on unchanged, got: %s", buf.String())
	}
}

func TestAlertsCountsWithAttribute(t *testing.T) {
	log, kinds, _ := alertLogger()

	scoped := log.With("module", "binding", logx.AlertKey, "binding_unanswered")
	scoped.Warn("first")
	scoped.Warn("second")

	if strings.Join(*kinds, ",") != "binding_unanswered,binding_unanswered" {
		t.Errorf("kinds = %v, want the With attribute counted on every record", *kinds)
	}
}

func TestAlertsRecordOverridesWith(t *testing.T) {
	log, kinds, _ := alertLogger()

	log.With(logx.AlertKey, "outer").Error("failed", logx.AlertKey, "inner")

	if strings.Join(*kinds, ",") != "inner" {
		t.Errorf("kinds = %v, want [inner]", *kinds)
	}
}

func TestAlertsIgnoresGroupedAttributes(t *testing.T) {
	log, kinds, _ := alertLogger()

	log.WithGroup("request").Error("failed", logx.AlertKey, "nested")
	log.Error("failed", slog.Group("request", logx.AlertKey, "nested"))

	if len(*kinds) != 0 {
		t.Errorf("kinds = %v, attributes inside a group are not alerts", *kinds)
	}
}

func TestAlertsKeepsKindBeforeGroup(t *testing.T) {
	log, kinds, _ := alertLogger()

	log.With(logx.AlertKey, "code_send_failed").WithGroup("provider").Error("failed", "status", 500)

	if strings.Join(*kinds, ",") != "code_send_failed" {
		t.Errorf("kinds = %v, want the kind set before the group", *kinds)
	}
}

func TestAlertsCountedBelowLevel(t *testing.T) {
	var buf bytes.Buffer
	var kinds []string
	h := logx.Alerts(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}), func(kind string) { kinds = append(kinds, kind) })
	log := slog.New(h)

	log.Warn("counted, not written", logx.AlertKey, "sms_unavailable")
	log.Debug("debug stays free", logx.AlertKey, "never")

	if strings.Join(kinds, ",") != "sms_unavailable" {
		t.Errorf("kinds = %v, want [sms_unavailable]", kinds)
	}
	if buf.Len() != 0 {
		t.Errorf("a record below the level must not be written, got: %s", buf.String())
	}
}
