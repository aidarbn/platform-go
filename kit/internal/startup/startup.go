// Package startup retries the check a module makes of an external service while the
// service starts: a database or a storage that comes up a moment after the instance,
// as happens in a rolling deploy, does not stop the start.
package startup

import (
	"context"
	"log/slog"
	"time"
)

// Backoff bounds: the first pause after a failure and the longest one.
const (
	firstWait = 250 * time.Millisecond
	maxWait   = 2 * time.Second
)

// Retry runs try until it succeeds, fails with an error permanent reports, the parent
// context ends, or budget runs out. The first attempt always runs; no new attempt starts
// once budget has passed since the first one, so a zero budget is a single attempt.
// Each attempt bounds itself; Retry does not cut a running attempt short. Failed
// attempts that are retried are logged at warn level.
func Retry(ctx context.Context, log *slog.Logger, what string, budget time.Duration, try func(context.Context) error, permanent func(error) bool) error {
	deadline := time.Now().Add(budget)
	wait := firstWait
	for attempt := 1; ; attempt++ {
		err := try(ctx)
		if err == nil {
			if attempt > 1 && log != nil {
				log.Info(what+" is reachable", "attempts", attempt)
			}
			return nil
		}
		if ctx.Err() != nil || (permanent != nil && permanent(err)) {
			return err
		}
		left := time.Until(deadline)
		if left <= 0 {
			return err
		}
		pause := min(wait, left)
		if log != nil {
			log.Warn(what+" is unreachable, retrying", "attempt", attempt, "retry_in", pause, "err", err)
		}
		t := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			t.Stop()
			return err
		case <-t.C:
		}
		wait = min(wait*2, maxWait)
	}
}
