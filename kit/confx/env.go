// Package confx читает настройки из переменных окружения.
//
// Все ошибки собираются и возвращаются разом: приложение падает с полным списком
// проблем, а не по одной за запуск.
package confx

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Loader читает переменные с общим префиксом и накапливает ошибки.
type Loader struct {
	prefix string
	errs   []error
}

// New создаёт загрузчик. Префикс может быть пустым.
func New(prefix string) *Loader { return &Loader{prefix: prefix} }

// Key возвращает полное имя переменной с префиксом.
func (l *Loader) Key(name string) string {
	if l.prefix == "" {
		return name
	}
	return l.prefix + "_" + name
}

func (l *Loader) lookup(name string) (string, bool) {
	v, ok := os.LookupEnv(l.Key(name))
	if !ok {
		return "", false
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	return v, true
}

// String возвращает значение или def, если переменная не задана.
func (l *Loader) String(name, def string) string {
	if v, ok := l.lookup(name); ok {
		return v
	}
	return def
}

// Required возвращает значение, а если переменной нет — запоминает ошибку.
func (l *Loader) Required(name string) string {
	if v, ok := l.lookup(name); ok {
		return v
	}
	l.errs = append(l.errs, fmt.Errorf("%s: обязательная переменная не задана", l.Key(name)))
	return ""
}

// Int читает целое число.
func (l *Loader) Int(name string, def int) int {
	v, ok := l.lookup(name)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.invalid(name, v, "целое число")
		return def
	}
	return n
}

// Bool читает true или false.
func (l *Loader) Bool(name string, def bool) bool {
	v, ok := l.lookup(name)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.invalid(name, v, "true или false")
		return def
	}
	return b
}

// Duration читает длительность в формате Go, например 30s или 5m.
func (l *Loader) Duration(name string, def time.Duration) time.Duration {
	v, ok := l.lookup(name)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.invalid(name, v, "длительность, например 30s")
		return def
	}
	return d
}

// Strings читает список через запятую.
func (l *Loader) Strings(name string, def []string) []string {
	v, ok := l.lookup(name)
	if !ok {
		return def
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return def
	}
	return out
}

func (l *Loader) invalid(name, value, want string) {
	l.errs = append(l.errs, fmt.Errorf("%s=%q: ожидается %s", l.Key(name), value, want))
}

// Err возвращает все накопленные ошибки одной.
func (l *Loader) Err() error { return errors.Join(l.errs...) }
