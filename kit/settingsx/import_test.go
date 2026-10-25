package settingsx_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aidarbn/platform-go/kit/settingsx"
)

// recordingRepo is a memory repository that remembers who wrote each value and can be
// told to fail a write.
type recordingRepo struct {
	*settingsx.MemoryRepo

	mu       sync.Mutex
	actors   map[string]string
	saves    int
	failSave string // key whose Save fails
}

func newRecordingRepo(values map[string]string) *recordingRepo {
	return &recordingRepo{MemoryRepo: settingsx.NewMemoryRepo(values), actors: map[string]string{}}
}

func (r *recordingRepo) Save(ctx context.Context, key, value, actor string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if key == r.failSave {
		return errors.New("disk full")
	}
	r.saves++
	r.actors[key] = actor
	return r.MemoryRepo.Save(ctx, key, value, actor)
}

// A source that writes a row for every key, defaults included, in its own spelling:
// only the real overrides are stored.
func TestImportStoresOnlyOverrides(t *testing.T) {
	ctx := context.Background()
	repo := newRecordingRepo(nil)
	s := settingsx.NewStore(testSchema(t), repo, nil)

	report, err := s.Import(ctx, map[string]string{
		"orders.create.max_attempts": "05",         // the default 5 in another spelling
		"orders.create.timeout":      "1m",         // differs from 30s
		"orders.cleanup.enabled":     "t",          // the default true
		"orders.cleanup.schedule":    "0  3 * * *", // the default with extra spaces
		"orders.create.mode":         "slow",       // differs from fast
		"orders.legacy.flag":         "true",       // removed from the schema
		"orders.create.typo":         "x",          // never was in the schema
	}, "import:app_configs")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	want := settingsx.ImportReport{
		Imported: []string{"orders.create.mode", "orders.create.timeout"},
		Defaults: []string{"orders.cleanup.enabled", "orders.cleanup.schedule", "orders.create.max_attempts"},
		Unknown:  []string{"orders.create.typo", "orders.legacy.flag"},
	}
	assertReport(t, report, want)
	if report.Total() != 7 {
		t.Errorf("Total = %d", report.Total())
	}

	stored, _ := repo.Load(ctx)
	if len(stored) != 2 || stored["orders.create.timeout"] != "1m" || stored["orders.create.mode"] != "slow" {
		t.Errorf("stored = %v", stored)
	}
	if repo.actors["orders.create.timeout"] != "import:app_configs" {
		t.Errorf("actors = %v", repo.actors)
	}
	// The store reads the imported values right away.
	if got := s.String("orders.create.mode"); got != "slow" {
		t.Errorf("mode = %q", got)
	}
}

// A repeated import stores nothing, and a value an administrator changed after the first
// import is kept.
func TestImportIsIdempotent(t *testing.T) {
	ctx := context.Background()
	repo := newRecordingRepo(nil)
	s := settingsx.NewStore(testSchema(t), repo, nil)
	source := map[string]string{
		"orders.create.timeout":      "60s",
		"orders.create.mode":         "slow",
		"orders.create.max_attempts": "5",
	}

	if _, err := s.Import(ctx, source, "import"); err != nil {
		t.Fatalf("first Import: %v", err)
	}
	if err := s.Set(ctx, "orders.create.mode", "fast", "admin"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	saves := repo.saves

	source["orders.create.timeout"] = "1m" // the same value in another spelling
	report, err := s.Import(ctx, source, "import")
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}
	assertReport(t, report, settingsx.ImportReport{
		Unchanged: []string{"orders.create.timeout"},
		Defaults:  []string{"orders.create.max_attempts"},
		Kept:      []string{"orders.create.mode"},
	})
	if repo.saves != saves {
		t.Errorf("a repeated import wrote %d values", repo.saves-saves)
	}
	if got := s.String("orders.create.mode"); got != "fast" {
		t.Errorf("mode = %q, the administrator's value must win", got)
	}
}

// An override stored by another instance is seen even though this store has not
// refreshed yet.
func TestImportReloadsBeforeComparing(t *testing.T) {
	ctx := context.Background()
	repo := newRecordingRepo(nil)
	s := settingsx.NewStore(testSchema(t), repo, nil)
	if err := s.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	_ = repo.MemoryRepo.Save(ctx, "orders.create.max_attempts", "9", "other instance")

	// Equal to the default, yet another instance has stored an override: it is kept.
	report, err := s.Import(ctx, map[string]string{"orders.create.max_attempts": "5"}, "import")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	assertReport(t, report, settingsx.ImportReport{Kept: []string{"orders.create.max_attempts"}})
}

func TestImportRejectsInvalidValues(t *testing.T) {
	ctx := context.Background()
	repo := newRecordingRepo(nil)
	s := settingsx.NewStore(testSchema(t), repo, nil)

	report, err := s.Import(ctx, map[string]string{
		"orders.create.max_attempts": "50",     // above max 20
		"orders.create.timeout":      "thirty", // not a duration
		"orders.create.mode":         "turbo",  // not an option
		"orders.cleanup.schedule":    "",       // empty schedule
	}, "import")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(report.Imported) != 0 || len(report.Rejected) != 4 {
		t.Fatalf("report = %+v", report)
	}
	keys := make([]string, 0, len(report.Rejected))
	for _, rej := range report.Rejected {
		keys = append(keys, rej.Key)
		if rej.Err == nil {
			t.Errorf("%s: no reason", rej.Key)
		}
	}
	if !slices.IsSorted(keys) {
		t.Errorf("rejected keys are not sorted: %v", keys)
	}
	if rej := report.Rejected[1]; rej.Key != "orders.create.max_attempts" || rej.Value != "50" ||
		!strings.Contains(rej.Err.Error(), "above the maximum") {
		t.Errorf("rejection = %+v", rej)
	}
	if stored, _ := repo.Load(ctx); len(stored) != 0 {
		t.Errorf("stored = %v", stored)
	}
}

func TestImportReportsRepositoryErrors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("connection refused")
	s := settingsx.NewStore(testSchema(t), failingRepo{err: boom}, nil)
	if _, err := s.Import(ctx, map[string]string{"orders.create.mode": "slow"}, "import"); !errors.Is(err, boom) {
		t.Errorf("load failure: err = %v", err)
	}

	// A write fails halfway: the keys stored before it are in the report, and a repeated
	// import carries on.
	repo := newRecordingRepo(nil)
	repo.failSave = "orders.create.timeout"
	s = settingsx.NewStore(testSchema(t), repo, nil)
	source := map[string]string{"orders.create.mode": "slow", "orders.create.timeout": "1m"}

	report, err := s.Import(ctx, source, "import")
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v", err)
	}
	assertReport(t, report, settingsx.ImportReport{Imported: []string{"orders.create.mode"}})

	repo.failSave = ""
	report, err = s.Import(ctx, source, "import")
	if err != nil {
		t.Fatalf("repeated Import: %v", err)
	}
	assertReport(t, report, settingsx.ImportReport{
		Imported:  []string{"orders.create.timeout"},
		Unchanged: []string{"orders.create.mode"},
	})
}

func TestImportReportString(t *testing.T) {
	report := settingsx.ImportReport{
		Imported: []string{"a.x", "a.y"},
		Defaults: []string{"a.z"},
		Rejected: []settingsx.ImportRejection{{Key: "a.w", Value: "50", Err: errors.New("too big")}},
	}
	got := report.String()
	for _, want := range []string{
		"4 keys: 2 imported, 0 unchanged, 1 defaults, 0 kept, 1 rejected, 0 unknown",
		"imported: a.x, a.y",
		"defaults: a.z",
		`rejected: a.w = "50": too big`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "unknown:") || strings.Contains(got, "kept:") {
		t.Errorf("empty lists must not be printed:\n%s", got)
	}
	if got := (settingsx.ImportReport{}).String(); got != "settings import: 0 keys: 0 imported, 0 unchanged, 0 defaults, 0 kept, 0 rejected, 0 unknown" {
		t.Errorf("empty report = %q", got)
	}
}

func assertReport(t *testing.T, got, want settingsx.ImportReport) {
	t.Helper()
	for _, l := range []struct {
		name      string
		got, want []string
	}{
		{"imported", got.Imported, want.Imported},
		{"unchanged", got.Unchanged, want.Unchanged},
		{"defaults", got.Defaults, want.Defaults},
		{"kept", got.Kept, want.Kept},
		{"unknown", got.Unknown, want.Unknown},
	} {
		if !slices.Equal(l.got, l.want) {
			t.Errorf("%s = %v, want %v", l.name, l.got, l.want)
		}
	}
	if len(got.Rejected) != len(want.Rejected) {
		t.Errorf("rejected = %+v", got.Rejected)
	}
}
