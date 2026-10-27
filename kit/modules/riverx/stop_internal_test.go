package riverx

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aidarbn/platform-go/kit/confx"
)

// fakeClient stands in for the River client: its jobs take run to finish on their own
// and cancelled to return once their context is cancelled.
type fakeClient struct {
	run, cancelled time.Duration
	stopErr        error

	start      time.Time
	softWaited time.Duration
	hardCalled bool
	hardLeft   time.Duration // time left before the deadline of the hard stop; -1 without one
}

func (c *fakeClient) Stop(ctx context.Context) error {
	if c.stopErr != nil {
		return c.stopErr
	}
	select {
	case <-time.After(c.run):
		c.softWaited = time.Since(c.start)
		return nil
	case <-ctx.Done():
		c.softWaited = time.Since(c.start)
		return ctx.Err()
	}
}

func (c *fakeClient) StopAndCancel(ctx context.Context) error {
	c.hardCalled = true
	c.hardLeft = -1
	if dl, ok := ctx.Deadline(); ok {
		c.hardLeft = time.Until(dl)
	}
	select {
	case <-time.After(c.cancelled):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestStopClientBudgets(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	for _, tc := range []struct {
		name           string
		shutdown       time.Duration // deadline of the context Stop gets
		soft, hard     time.Duration
		client         fakeClient
		wantSoft       time.Duration
		wantHard       bool
		wantHardLeft   time.Duration
		wantErr        error
		wantErrContain string
	}{
		{
			name: "jobs finish within the soft budget", shutdown: 20 * time.Second, soft: 6 * time.Second, hard: time.Second,
			client: fakeClient{run: 2 * time.Second}, wantSoft: 2 * time.Second,
		},
		{
			name: "taply: 6s soft, then 1s for cancelled jobs", shutdown: 20 * time.Second, soft: 6 * time.Second, hard: time.Second,
			client: fakeClient{run: time.Minute, cancelled: 300 * time.Millisecond}, wantSoft: 6 * time.Second,
			wantHard: true, wantHardLeft: time.Second,
		},
		{
			name: "cancelled jobs that do not return are left behind", shutdown: 20 * time.Second, soft: 6 * time.Second, hard: time.Second,
			client: fakeClient{run: time.Minute, cancelled: time.Minute}, wantSoft: 6 * time.Second,
			wantHard: true, wantHardLeft: time.Second, wantErr: context.DeadlineExceeded,
		},
		{
			name: "no budgets: the shutdown timeout, then no limit, as before", shutdown: 20 * time.Second,
			client: fakeClient{run: time.Minute, cancelled: 5 * time.Second}, wantSoft: 20 * time.Second,
			wantHard: true, wantHardLeft: -1,
		},
		{
			name: "the shutdown timeout bounds a longer soft budget", shutdown: 3 * time.Second, soft: 6 * time.Second, hard: time.Second,
			client: fakeClient{run: time.Minute}, wantSoft: 3 * time.Second, wantHard: true, wantHardLeft: time.Second,
		},
		{
			name: "another error of the soft stop is returned", shutdown: 20 * time.Second, soft: time.Second,
			client: fakeClient{stopErr: errors.New("connection reset")}, wantErrContain: "connection reset",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), tc.shutdown)
				defer cancel()
				c := tc.client
				c.start = time.Now()

				err := stopClient(ctx, &c, tc.soft, tc.hard, log)
				switch {
				case tc.wantErrContain != "":
					if err == nil || !strings.Contains(err.Error(), tc.wantErrContain) {
						t.Fatalf("err = %v", err)
					}
					if c.hardCalled {
						t.Error("an error other than the deadline cancelled the jobs")
					}
					return
				case !errors.Is(err, tc.wantErr) && (tc.wantErr != nil || err != nil):
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if c.softWaited != tc.wantSoft {
					t.Errorf("soft stop waited %s, want %s", c.softWaited, tc.wantSoft)
				}
				if c.hardCalled != tc.wantHard {
					t.Fatalf("hard stop called: %v, want %v", c.hardCalled, tc.wantHard)
				}
				if tc.wantHard && c.hardLeft != tc.wantHardLeft {
					t.Errorf("hard stop budget %s, want %s", c.hardLeft, tc.wantHardLeft)
				}
			})
		})
	}
}

// Queues set in the environment or in code win over QueuesFrom, which wins over the
// default.
func TestQueuesPrecedence(t *testing.T) {
	ctx := context.Background()
	fromApp := func(context.Context) (map[string]int, error) { return map[string]int{"sync": 4, "payments": 3}, nil }

	for _, tc := range []struct {
		name string
		cfg  Config
		fn   func(context.Context) (map[string]int, error)
		want string
	}{
		{name: "default", cfg: Config{}, want: "default=10"},
		{name: "default replaced by the application", cfg: Config{}, fn: fromApp, want: "payments=3,sync=4"},
		{name: "code wins", cfg: Config{Queues: map[string]int{"default": 2}}, fn: fromApp, want: "default=2"},
		{name: "environment wins", cfg: loaded(t, "default=5"), fn: fromApp, want: "default=5"},
		{name: "unset environment leaves it to the application", cfg: loaded(t, ""), fn: fromApp, want: "payments=3,sync=4"},
		{name: "code over the loaded default wins", cfg: func() Config { c := loaded(t, ""); c.Queues = map[string]int{"mail": 2}; return c }(), fn: fromApp, want: "mail=2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := New(tc.cfg).queues(ctx, tc.fn)
			if err != nil {
				t.Fatal(err)
			}
			if s := formatQueues(got); s != tc.want {
				t.Errorf("queues = %s, want %s", s, tc.want)
			}
		})
	}

	for name, fn := range map[string]func(context.Context) (map[string]int, error){
		"an error":     func(context.Context) (map[string]int, error) { return nil, errors.New("settings are gone") },
		"no queues":    func(context.Context) (map[string]int, error) { return map[string]int{}, nil },
		"zero workers": func(context.Context) (map[string]int, error) { return map[string]int{"sync": 0}, nil },
		"no name":      func(context.Context) (map[string]int, error) { return map[string]int{" ": 1}, nil },
	} {
		if _, err := New(Config{}).queues(ctx, fn); err == nil || !strings.Contains(err.Error(), "river queues") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func loaded(t *testing.T, queues string) Config {
	t.Helper()
	t.Setenv("RIVER_QUEUES", queues)
	l := confx.New("")
	cfg := Load(l)
	if err := l.Err(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func formatQueues(q map[string]int) string {
	var parts []string
	for name, n := range q {
		parts = append(parts, name+"="+strconv.Itoa(n))
	}
	slices.Sort(parts)
	return strings.Join(parts, ",")
}
