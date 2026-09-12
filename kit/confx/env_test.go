package confx_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aidarbn/platform-go/kit/confx"
)

func TestDefaults(t *testing.T) {
	l := confx.New("APP")

	if got := l.String("HOST", "localhost"); got != "localhost" {
		t.Errorf("String = %q", got)
	}
	if got := l.Int("PORT", 8080); got != 8080 {
		t.Errorf("Int = %d", got)
	}
	if got := l.Bool("DEBUG", true); !got {
		t.Errorf("Bool = %v", got)
	}
	if got := l.Duration("TIMEOUT", time.Second); got != time.Second {
		t.Errorf("Duration = %v", got)
	}
	if err := l.Err(); err != nil {
		t.Errorf("Err = %v, want nil", err)
	}
}

func TestReadsValues(t *testing.T) {
	t.Setenv("APP_HOST", " db ") // surrounding spaces are trimmed
	t.Setenv("APP_PORT", "5432")
	t.Setenv("APP_DEBUG", "false")
	t.Setenv("APP_TIMEOUT", "1m30s")
	t.Setenv("APP_QUEUES", "default, notify ,")

	l := confx.New("APP")

	if got := l.String("HOST", "localhost"); got != "db" {
		t.Errorf("String = %q", got)
	}
	if got := l.Int("PORT", 0); got != 5432 {
		t.Errorf("Int = %d", got)
	}
	if got := l.Bool("DEBUG", true); got {
		t.Errorf("Bool = %v", got)
	}
	if got := l.Duration("TIMEOUT", 0); got != 90*time.Second {
		t.Errorf("Duration = %v", got)
	}
	if got := l.Strings("QUEUES", nil); len(got) != 2 || got[0] != "default" || got[1] != "notify" {
		t.Errorf("Strings = %q", got)
	}
	if err := l.Err(); err != nil {
		t.Errorf("Err = %v, want nil", err)
	}
}

func TestEmptyValueIsAbsent(t *testing.T) {
	t.Setenv("APP_HOST", "   ")

	l := confx.New("APP")
	if got := l.String("HOST", "localhost"); got != "localhost" {
		t.Errorf("a blank value must count as unset, got %q", got)
	}
}

func TestCollectsAllErrors(t *testing.T) {
	t.Setenv("APP_PORT", "not a number")
	t.Setenv("APP_TIMEOUT", "forever")

	l := confx.New("APP")
	l.Required("DATABASE_URL")
	l.Int("PORT", 8080)
	l.Duration("TIMEOUT", time.Second)

	err := l.Err()
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"APP_DATABASE_URL", "APP_PORT", "APP_TIMEOUT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestNoPrefix(t *testing.T) {
	t.Setenv("HOST", "db")

	l := confx.New("")
	if got := l.Key("HOST"); got != "HOST" {
		t.Errorf("Key = %q", got)
	}
	if got := l.String("HOST", ""); got != "db" {
		t.Errorf("String = %q", got)
	}
}
