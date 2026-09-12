package settingsx_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aidarbn/platform-go/kit/settingsx"
)

func testSchema(t *testing.T) settingsx.Schema {
	t.Helper()
	schema, err := settingsx.NewSchema(
		settingsx.Definition{Key: "orders.create.max_attempts", Group: "orders.create", Name: "max_attempts", Kind: settingsx.KindInt, Default: "5", Min: "1", Max: "20"},
		settingsx.Definition{Key: "orders.create.timeout", Group: "orders.create", Name: "timeout", Kind: settingsx.KindDuration, Default: "30s"},
		settingsx.Definition{Key: "orders.cleanup.enabled", Group: "orders.cleanup", Name: "enabled", Kind: settingsx.KindBool, Default: "true"},
		settingsx.Definition{Key: "orders.cleanup.schedule", Group: "orders.cleanup", Name: "schedule", Kind: settingsx.KindCron, Default: "0 3 * * *"},
		settingsx.Definition{Key: "orders.create.mode", Group: "orders.create", Name: "mode", Kind: settingsx.KindString, Default: "fast", Options: []string{"fast", "slow"}},
	)
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}
	return schema
}

func TestStoreFallsBackToDefaults(t *testing.T) {
	s := settingsx.NewTestStore(testSchema(t), nil)

	if got := s.Int("orders.create.max_attempts"); got != 5 {
		t.Errorf("max_attempts = %d", got)
	}
	if got := s.Duration("orders.create.timeout"); got != 30*time.Second {
		t.Errorf("timeout = %s", got)
	}
	if got := s.Bool("orders.cleanup.enabled"); !got {
		t.Error("enabled = false")
	}
	if got := s.Cron("orders.cleanup.schedule"); got != "0 3 * * *" {
		t.Errorf("schedule = %q", got)
	}
	if got := s.String("orders.create.mode"); got != "fast" {
		t.Errorf("mode = %q", got)
	}
	if got := s.Overrides(); got != 0 {
		t.Errorf("overrides = %d", got)
	}
}

func TestStoreReadsOverrides(t *testing.T) {
	s := settingsx.NewTestStore(testSchema(t), map[string]string{
		"orders.create.max_attempts": "7",
		"orders.cleanup.enabled":     "false",
	})

	if got := s.Int("orders.create.max_attempts"); got != 7 {
		t.Errorf("max_attempts = %d", got)
	}
	if s.Bool("orders.cleanup.enabled") {
		t.Error("enabled = true")
	}
	if got := s.Duration("orders.create.timeout"); got != 30*time.Second {
		t.Errorf("timeout = %s: an untouched setting must follow the default", got)
	}
	if got := s.Overrides(); got != 2 {
		t.Errorf("overrides = %d", got)
	}
}

// A value the schema no longer describes, or one that stopped being valid, must not
// break the application: it is ignored and the default applies.
func TestStoreIgnoresUnusableStoredValues(t *testing.T) {
	s := settingsx.NewTestStore(testSchema(t), map[string]string{
		"orders.create.max_attempts": "500",  // above the maximum
		"orders.create.timeout":      "soon", // not a duration
		"orders.removed.flag":        "true", // not in the schema any more
	})

	if got := s.Int("orders.create.max_attempts"); got != 5 {
		t.Errorf("max_attempts = %d", got)
	}
	if got := s.Duration("orders.create.timeout"); got != 30*time.Second {
		t.Errorf("timeout = %s", got)
	}
	if got := s.Overrides(); got != 0 {
		t.Errorf("overrides = %d", got)
	}
}

func TestStoreSetAndReset(t *testing.T) {
	ctx := context.Background()
	repo := settingsx.NewMemoryRepo(nil)
	s := settingsx.NewStore(testSchema(t), repo, nil)
	if err := s.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if err := s.Set(ctx, "orders.create.max_attempts", "9", "admin"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := s.Int("orders.create.max_attempts"); got != 9 {
		t.Errorf("max_attempts = %d", got)
	}

	// The value survives a reload, which means it reached the repository.
	if err := s.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := s.Int("orders.create.max_attempts"); got != 9 {
		t.Errorf("after reload max_attempts = %d", got)
	}

	if err := s.Reset(ctx, "orders.create.max_attempts", "admin"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if got := s.Int("orders.create.max_attempts"); got != 5 {
		t.Errorf("after reset max_attempts = %d", got)
	}
	stored, _ := repo.Load(ctx)
	if _, ok := stored["orders.create.max_attempts"]; ok {
		t.Error("reset must remove the override from the repository")
	}
}

func TestStoreSetRejectsBadValues(t *testing.T) {
	ctx := context.Background()
	repo := settingsx.NewMemoryRepo(nil)
	s := settingsx.NewStore(testSchema(t), repo, nil)

	for key, raw := range map[string]string{
		"orders.create.max_attempts": "0",
		"orders.create.mode":         "medium",
		"orders.cleanup.schedule":    "nightly",
		"orders.unknown":             "1",
	} {
		if err := s.Set(ctx, key, raw, "admin"); err == nil {
			t.Errorf("Set(%s, %s) accepted a bad value", key, raw)
		}
	}

	stored, _ := repo.Load(ctx)
	if len(stored) != 0 {
		t.Errorf("a rejected value must not be stored: %v", stored)
	}
}

// failingRepo reports a broken database.
type failingRepo struct{ err error }

func (r failingRepo) Load(context.Context) (map[string]string, error) { return nil, r.err }
func (r failingRepo) Save(context.Context, string, string, string) error {
	return r.err
}
func (r failingRepo) Delete(context.Context, string) error { return r.err }

func TestStoreReportsRepositoryErrors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("connection refused")
	s := settingsx.NewStore(testSchema(t), failingRepo{err: boom}, nil)

	if err := s.Reload(ctx); !errors.Is(err, boom) {
		t.Errorf("Reload: %v", err)
	}
	err := s.Set(ctx, "orders.create.max_attempts", "7", "admin")
	if !errors.Is(err, boom) {
		t.Errorf("Set: %v", err)
	}
	// A failed write must not change what the application reads.
	if got := s.Int("orders.create.max_attempts"); got != 5 {
		t.Errorf("max_attempts = %d", got)
	}
	if err := s.Reset(ctx, "orders.create.max_attempts", "admin"); !errors.Is(err, boom) {
		t.Errorf("Reset: %v", err)
	}
}

func TestStoreValues(t *testing.T) {
	s := settingsx.NewTestStore(testSchema(t), map[string]string{"orders.create.timeout": "45s"})

	values := s.Values()
	if len(values) != 5 {
		t.Fatalf("values = %d", len(values))
	}
	if values[0].Key != "orders.cleanup.enabled" {
		t.Errorf("values are not ordered by key: %+v", values)
	}

	for _, v := range values {
		switch v.Key {
		case "orders.create.timeout":
			if v.Value != "45s" || !v.Overridden {
				t.Errorf("timeout = %+v", v)
			}
		case "orders.create.max_attempts":
			if v.Value != "5" || v.Overridden {
				t.Errorf("max_attempts = %+v", v)
			}
		}
	}
}

func TestStoreWatch(t *testing.T) {
	ctx := context.Background()
	repo := settingsx.NewMemoryRepo(nil)
	s := settingsx.NewStore(testSchema(t), repo, nil)

	var changed [][]string
	s.Watch(func(keys []string) { changed = append(changed, keys) })

	if err := s.Set(ctx, "orders.cleanup.schedule", "@daily", "admin"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// Setting the same value again changes nothing, so it must not notify.
	if err := s.Set(ctx, "orders.cleanup.schedule", "@daily", "admin"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := s.Reset(ctx, "orders.cleanup.schedule", "admin"); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	// A reload that brings nothing new must not notify either.
	if err := s.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if len(changed) != 2 {
		t.Fatalf("notifications = %v", changed)
	}
	for _, keys := range changed {
		if len(keys) != 1 || keys[0] != "orders.cleanup.schedule" {
			t.Errorf("keys = %v", keys)
		}
	}
}

func TestStoreWatchSeesReloadFromAnotherInstance(t *testing.T) {
	ctx := context.Background()
	repo := settingsx.NewMemoryRepo(nil)
	s := settingsx.NewStore(testSchema(t), repo, nil)
	if err := s.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	var changed []string
	s.Watch(func(keys []string) { changed = append(changed, keys...) })

	// Another instance wrote a value straight into the repository.
	if err := repo.Save(ctx, "orders.create.timeout", "1m", "other"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if len(changed) != 1 || changed[0] != "orders.create.timeout" {
		t.Fatalf("changed = %v", changed)
	}
	if got := s.Duration("orders.create.timeout"); got != time.Minute {
		t.Errorf("timeout = %s", got)
	}
}

func TestStorePanicsOnMisuse(t *testing.T) {
	s := settingsx.NewTestStore(testSchema(t), nil)

	cases := map[string]func(){
		"unknown key": func() { s.Int("orders.create.nope") },
		"wrong type":  func() { s.Bool("orders.create.max_attempts") },
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("want a panic")
				}
			}()
			fn()
		})
	}
}

func TestStoreRawUnknownKeyPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "unknown setting") {
			t.Fatalf("recover = %v", r)
		}
	}()
	settingsx.NewTestStore(testSchema(t), nil).Raw("nope")
}

// Settings are read on every request while the refresh loop and the admin UI write:
// the race detector must stay quiet.
func TestStoreConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	s := settingsx.NewStore(testSchema(t), settingsx.NewMemoryRepo(nil), nil)
	if err := s.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for range 50 {
				switch i % 4 {
				case 0:
					_ = s.Int("orders.create.max_attempts")
				case 1:
					_ = s.Values()
				case 2:
					_ = s.Set(ctx, "orders.create.max_attempts", "7", "admin")
				case 3:
					_ = s.Reload(ctx)
				}
			}
		}(i)
	}
	wg.Wait()
}
