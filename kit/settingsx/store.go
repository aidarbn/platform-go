package settingsx

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aidarbn/platform-go/kit/platform"
)

// Repo stores the values that differ from the defaults. Only overrides are kept, so a
// setting left alone keeps following the default from the schema across releases.
type Repo interface {
	Load(ctx context.Context) (map[string]string, error)
	Save(ctx context.Context, key, value, actor string) error
	Delete(ctx context.Context, key string) error
}

// Store keeps the current values in memory and reads them from the repository.
//
// Reads happen on every request and must not touch the database, so values are cached;
// the module refreshes the cache and Set updates it right away.
type Store struct {
	schema Schema
	repo   Repo
	log    *slog.Logger

	mu        sync.RWMutex
	overrides map[string]string
	watchers  []func(changed []string)
}

// Value is a setting together with its current value: what the admin UI shows.
type Value struct {
	Definition
	Value      string // current value
	Overridden bool   // true when it differs from the default and is stored in the repository
}

// NewStore creates a store. The values are not loaded yet: call Reload, which the
// module does during Init. The logger may be nil.
func NewStore(schema Schema, repo Repo, log *slog.Logger) *Store {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Store{
		schema:    schema,
		repo:      repo,
		log:       log,
		overrides: make(map[string]string),
	}
}

// Schema returns the schema of the settings.
func (s *Store) Schema() Schema { return s.schema }

// Reload reads the overrides from the repository and replaces the cache. Values the
// schema does not know are ignored: a removed setting must not break the application.
func (s *Store) Reload(ctx context.Context) error {
	stored, err := s.repo.Load(ctx)
	if err != nil {
		return fmt.Errorf("settings: load: %w", err)
	}

	fresh := make(map[string]string, len(stored))
	for key, raw := range stored {
		if err := s.schema.Validate(key, raw); err != nil {
			s.log.Warn("settings: stored value ignored", "key", key, "error", err)
			continue
		}
		fresh[key] = raw
	}

	s.mu.Lock()
	changed := diff(s.overrides, fresh)
	s.overrides = fresh
	s.mu.Unlock()

	s.notify(changed)
	return nil
}

// diff returns the keys whose effective value differs between two sets of overrides.
func diff(before, after map[string]string) []string {
	var changed []string
	for key, raw := range after {
		if before[key] != raw {
			changed = append(changed, key)
		}
	}
	for key := range before {
		if _, ok := after[key]; !ok {
			changed = append(changed, key)
		}
	}
	slices.Sort(changed)
	return changed
}

// Raw returns the current value as text. An unknown key is a programming error: the
// generated accessors only pass keys from the schema.
func (s *Store) Raw(key string) string {
	def, ok := s.schema.Definition(key)
	if !ok {
		panic(fmt.Sprintf("settingsx: unknown setting %q", key))
	}

	s.mu.RLock()
	raw, overridden := s.overrides[key]
	s.mu.RUnlock()

	if overridden {
		return raw
	}
	return def.Default
}

func (s *Store) raw(key string, want Kind) string {
	def, ok := s.schema.Definition(key)
	if !ok {
		panic(fmt.Sprintf("settingsx: unknown setting %q", key))
	}
	if def.Kind != want {
		panic(fmt.Sprintf("settingsx: setting %q has type %s, read as %s", key, def.Kind, want))
	}
	return s.Raw(key)
}

// Bool returns a bool setting.
func (s *Store) Bool(key string) bool {
	v, _ := strconv.ParseBool(s.raw(key, KindBool))
	return v
}

// Int returns an int setting.
func (s *Store) Int(key string) int {
	v, _ := strconv.Atoi(s.raw(key, KindInt))
	return v
}

// Int64 returns an int64 setting.
func (s *Store) Int64(key string) int64 {
	v, _ := strconv.ParseInt(s.raw(key, KindInt64), 10, 64)
	return v
}

// Float returns a float setting.
func (s *Store) Float(key string) float64 {
	v, _ := strconv.ParseFloat(s.raw(key, KindFloat), 64)
	return v
}

// Duration returns a duration setting.
func (s *Store) Duration(key string) time.Duration {
	v, _ := time.ParseDuration(s.raw(key, KindDuration))
	return v
}

// String returns a string setting.
func (s *Store) String(key string) string { return s.raw(key, KindString) }

// Cron returns a schedule setting as text: the scheduler parses it itself.
func (s *Store) Cron(key string) string { return s.raw(key, KindCron) }

// Set validates the value, stores it and updates the cache. The actor is recorded for
// the audit log: settings change behaviour in production, so who changed what matters.
func (s *Store) Set(ctx context.Context, key, raw, actor string) error {
	if err := s.schema.Validate(key, raw); err != nil {
		return err
	}
	if err := s.repo.Save(ctx, key, raw, actor); err != nil {
		return fmt.Errorf("settings: save %s: %w", key, err)
	}

	s.mu.Lock()
	changed := s.overrides[key] != raw
	s.overrides[key] = raw
	s.mu.Unlock()

	s.log.Info("settings: value changed", "key", key, "value", raw, "actor", actor)
	if changed {
		s.notify([]string{key})
	}
	return nil
}

// Reset drops the override so the setting follows the default from the schema again.
func (s *Store) Reset(ctx context.Context, key, actor string) error {
	if _, ok := s.schema.Definition(key); !ok {
		return fmt.Errorf("unknown setting %q", key)
	}
	if err := s.repo.Delete(ctx, key); err != nil {
		return fmt.Errorf("settings: reset %s: %w", key, err)
	}

	s.mu.Lock()
	_, existed := s.overrides[key]
	delete(s.overrides, key)
	s.mu.Unlock()

	s.log.Info("settings: value reset", "key", key, "actor", actor)
	if existed {
		s.notify([]string{key})
	}
	return nil
}

// Values returns every setting with its current value, ordered by key.
func (s *Store) Values() []Value {
	s.mu.RLock()
	overrides := make(map[string]string, len(s.overrides))
	for key, raw := range s.overrides {
		overrides[key] = raw
	}
	s.mu.RUnlock()

	defs := s.schema.Definitions()
	out := make([]Value, 0, len(defs))
	for _, def := range defs {
		value := Value{Definition: def, Value: def.Default}
		if raw, ok := overrides[def.Key]; ok {
			value.Value, value.Overridden = raw, true
		}
		out = append(out, value)
	}
	return out
}

// Overrides returns how many settings differ from their defaults.
func (s *Store) Overrides() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.overrides)
}

// Watch registers a callback for value changes: it is how a schedule or a limit takes
// effect without a restart. The callback runs in the goroutine that made the change,
// so it must be quick and must not call back into the store.
func (s *Store) Watch(fn func(changed []string)) {
	if fn == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watchers = append(s.watchers, fn)
}

func (s *Store) notify(changed []string) {
	if len(changed) == 0 {
		return
	}
	s.mu.RLock()
	watchers := slices.Clone(s.watchers)
	s.mu.RUnlock()

	for _, fn := range watchers {
		fn(changed)
	}
}

// From returns the store from the container. It panics when the settings module is not
// enabled, the same way every other platform dependency does.
func From(app *platform.App) *Store { return platform.Get[*Store](app) }

// GroupOf returns the group name of a key: "orders.create.timeout" gives "orders.create".
func GroupOf(key string) string {
	if i := strings.LastIndex(key, "."); i > 0 {
		return key[:i]
	}
	return ""
}
