package startup

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

var errDown = errors.New("down")

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRetrySucceedsAfterFailures(t *testing.T) {
	n := 0
	err := Retry(context.Background(), quiet(), "thing", 5*time.Second, func(context.Context) error {
		n++
		if n < 3 {
			return errDown
		}
		return nil
	}, nil)
	if err != nil || n != 3 {
		t.Fatalf("err = %v after %d attempts, want success on the third", err, n)
	}
}

func TestRetryZeroBudgetIsOneAttempt(t *testing.T) {
	n := 0
	err := Retry(context.Background(), nil, "thing", 0, func(context.Context) error { n++; return errDown }, nil)
	if !errors.Is(err, errDown) || n != 1 {
		t.Fatalf("err = %v after %d attempts, want errDown after one", err, n)
	}
}

func TestRetryStopsOnPermanentError(t *testing.T) {
	n := 0
	start := time.Now()
	err := Retry(context.Background(), quiet(), "thing", time.Minute, func(context.Context) error { n++; return errDown },
		func(err error) bool { return errors.Is(err, errDown) })
	if !errors.Is(err, errDown) || n != 1 || time.Since(start) > time.Second {
		t.Fatalf("err = %v after %d attempts in %s, want errDown at once", err, n, time.Since(start))
	}
}

func TestRetryGivesUpAfterBudget(t *testing.T) {
	n := 0
	start := time.Now()
	err := Retry(context.Background(), quiet(), "thing", 600*time.Millisecond, func(context.Context) error { n++; return errDown }, nil)
	took := time.Since(start)
	// 250ms, then 350ms to the deadline: three attempts.
	if !errors.Is(err, errDown) || n != 3 || took < 600*time.Millisecond || took > 2*time.Second {
		t.Fatalf("err = %v after %d attempts in %s, want errDown after three in about 600ms", err, n, took)
	}
}

func TestRetryStopsWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	start := time.Now()
	err := Retry(ctx, quiet(), "thing", time.Minute, func(context.Context) error { n++; return errDown }, nil)
	if !errors.Is(err, errDown) || time.Since(start) > time.Second {
		t.Fatalf("err = %v in %s, want errDown soon after the cancel", err, time.Since(start))
	}

	n = 0
	err = Retry(ctx, quiet(), "thing", time.Minute, func(ctx context.Context) error { n++; return ctx.Err() }, nil)
	if !errors.Is(err, context.Canceled) || n != 1 {
		t.Fatalf("err = %v after %d attempts, want Canceled after one", err, n)
	}
}

func TestRetryBackoffDoubles(t *testing.T) {
	var at []time.Time
	_ = Retry(context.Background(), nil, "thing", 2400*time.Millisecond, func(context.Context) error {
		at = append(at, time.Now())
		return errDown
	}, nil)
	// pauses 250ms, 500ms, 1s, then what is left of the budget (650ms).
	if len(at) != 5 {
		t.Fatalf("%d attempts, want 5", len(at))
	}
	for i, want := range []time.Duration{250, 500, 1000} {
		got := at[i+1].Sub(at[i])
		if got < want*time.Millisecond || got > want*time.Millisecond+300*time.Millisecond {
			t.Errorf("pause %d = %s, want about %dms", i+1, got, want)
		}
	}
}
