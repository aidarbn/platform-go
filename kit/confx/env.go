// Package confx reads settings from environment variables.
//
// Every problem is collected instead of returned immediately: the application fails
// with the full list of bad or missing variables rather than one per restart.
package confx

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Loader reads variables sharing a prefix and accumulates errors.
type Loader struct {
	prefix string
	errs   []error
}

// New creates a loader. The prefix may be empty.
func New(prefix string) *Loader { return &Loader{prefix: prefix} }

// Key returns the full variable name including the prefix.
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

// String returns the value or def when the variable is unset.
func (l *Loader) String(name, def string) string {
	if v, ok := l.lookup(name); ok {
		return v
	}
	return def
}

// Required returns the value and records an error when the variable is unset.
func (l *Loader) Required(name string) string {
	if v, ok := l.lookup(name); ok {
		return v
	}
	l.errs = append(l.errs, fmt.Errorf("%s: required variable is not set", l.Key(name)))
	return ""
}

// Int reads an integer.
func (l *Loader) Int(name string, def int) int {
	v, ok := l.lookup(name)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		l.invalid(name, v, "an integer")
		return def
	}
	return n
}

// Bool reads true or false.
func (l *Loader) Bool(name string, def bool) bool {
	v, ok := l.lookup(name)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.invalid(name, v, "true or false")
		return def
	}
	return b
}

// Duration reads a Go duration such as 30s or 5m.
func (l *Loader) Duration(name string, def time.Duration) time.Duration {
	v, ok := l.lookup(name)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		l.invalid(name, v, "a duration such as 30s")
		return def
	}
	return d
}

// Strings reads a comma separated list.
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
	l.errs = append(l.errs, fmt.Errorf("%s=%q: expected %s", l.Key(name), value, want))
}

// Err returns every collected error as one.
func (l *Loader) Err() error { return errors.Join(l.errs...) }
