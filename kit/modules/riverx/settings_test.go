package riverx_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/logx"
	"github.com/aidarbn/platform-go/kit/modules/postgres"
	"github.com/aidarbn/platform-go/kit/modules/riverx"
	"github.com/aidarbn/platform-go/kit/modules/settings"
	"github.com/aidarbn/platform-go/kit/platform"
	"github.com/aidarbn/platform-go/kit/settingsx"
)

func TestLoadStopBudgets(t *testing.T) {
	l := confx.New("")
	cfg := riverx.Load(l)
	if err := l.Err(); err != nil || cfg.StopTimeout != 0 || cfg.CancelTimeout != 0 {
		t.Fatalf("defaults: %+v, %v", cfg, err)
	}

	t.Setenv("RIVER_STOP_TIMEOUT", "6s")
	t.Setenv("RIVER_CANCEL_TIMEOUT", "1s")
	l = confx.New("")
	cfg = riverx.Load(l)
	if err := l.Err(); err != nil || cfg.StopTimeout != 6*time.Second || cfg.CancelTimeout != time.Second {
		t.Fatalf("values: %+v, %v", cfg, err)
	}

	t.Setenv("RIVER_CANCEL_TIMEOUT", "-1s")
	l = confx.New("")
	riverx.Load(l)
	if err := l.Err(); err == nil || !strings.Contains(err.Error(), "RIVER_CANCEL_TIMEOUT") {
		t.Fatalf("err = %v", err)
	}
}

func TestStopPhase(t *testing.T) {
	phases := riverx.New(riverx.Config{}).StopPhases()
	if len(phases) != 1 || phases[0].Name != "queue" || phases[0].Order != platform.PhaseQueue || phases[0].Stop == nil {
		t.Fatalf("phases = %+v", phases)
	}
}

func TestRegistrationMistakesPanic(t *testing.T) {
	app := platform.NewApp(nil)
	mustPanic(t, "QueuesFrom(nil)", func() { riverx.QueuesFrom(app, nil) })
	mustPanic(t, "MaxAttemptsFrom(nil)", func() { riverx.MaxAttemptsFrom(app, nil) })
}

func mustPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s did not panic", what)
		}
	}()
	fn()
}

// Queue sizes come from business settings, as taply's sync.queue_workers do: a queue
// the environment does not know is worked once the settings name it.
func TestQueuesFromSettings(t *testing.T) {
	dsn := database(t)
	t.Setenv("RIVER_QUEUES", "")
	cfg := riverx.Load(confx.New(""))
	cfg.JobTimeout = 10 * time.Second

	schema := settingsx.MustSchema(
		settingsx.Definition{Key: "mail.queue_workers", Group: "mail", Name: "queue_workers", Kind: settingsx.KindInt, Default: "2", RequiresRestart: true},
	)
	mail := &mailWorker{sent: make(chan string, 4)}
	var queue *riverx.Queue
	r := run(t, []platform.Module{
		postgres.New(postgres.Config{URL: dsn}),
		riverx.New(cfg),
		settings.New(settings.Config{}, schema), // after river, as a project lists it: settings load in Init
	}, func(app *platform.App) error {
		s := settingsx.From(app)
		riverx.AddWorker(app, mail)
		riverx.QueuesFrom(app, func(context.Context) (map[string]int, error) {
			return map[string]int{river.QueueDefault: 1, "mail": s.Int("mail.queue_workers")}, nil
		})
		mustPanic(t, "a second QueuesFrom", func() {
			riverx.QueuesFrom(app, func(context.Context) (map[string]int, error) { return nil, nil })
		})
		queue = riverx.QueueFrom(app)
		return nil
	})

	if _, err := queue.Insert(context.Background(), MailArgs{To: "settings"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-mail.sent:
		if got != "mail:settings" {
			t.Errorf("sent %q", got)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the queue named by the settings is not worked")
	}
	app := <-r.app
	mustPanic(t, "QueuesFrom after start", func() {
		riverx.QueuesFrom(app, func(context.Context) (map[string]int, error) { return nil, nil })
	})
	mustPanic(t, "MaxAttemptsFrom after start", func() {
		riverx.MaxAttemptsFrom(app, func(context.Context, string) (int, bool) { return 0, false })
	})
}

// A failing QueuesFrom stops the start: working with queues nobody chose is worse.
func TestQueuesFromErrorStopsTheStart(t *testing.T) {
	dsn := database(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := platform.RunContext(ctx, platform.Config{
		Service: "river-test", OpsAddr: "127.0.0.1:0", ShutdownTimeout: 5 * time.Second,
		Logger: logx.New(logx.Options{Writer: discard{}}),
	}, []platform.Module{postgres.New(postgres.Config{URL: dsn}), riverx.New(riverx.Config{Work: true})},
		func(app *platform.App) error {
			riverx.AddWorker(app, &pingWorker{seen: make(chan string, 1)})
			riverx.QueuesFrom(app, func(context.Context) (map[string]int, error) {
				return nil, errors.New("settings are gone")
			})
			return nil
		})
	if err == nil || !strings.Contains(err.Error(), "river queues: settings are gone") {
		t.Fatalf("err = %v", err)
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// MaxAttemptsFrom replaces the attempts a job carries at insert time, as taply reads
// create_order.max_attempts; attempts passed explicitly win.
func TestMaxAttemptsFrom(t *testing.T) {
	dsn := database(t)
	attempts := &atomic.Int64{}
	attempts.Store(7)

	var queue *riverx.Queue
	r := run(t, []platform.Module{
		postgres.New(postgres.Config{URL: dsn}),
		riverx.New(riverx.Config{Work: false}),
	}, func(app *platform.App) error {
		queue = riverx.QueueFrom(app)
		riverx.MaxAttemptsFrom(app, func(_ context.Context, kind string) (int, bool) {
			if kind == (MailArgs{}).Kind() {
				return int(attempts.Load()), true
			}
			if kind == (TickArgs{}).Kind() {
				return 0, true // a setting left at zero keeps what the job carries
			}
			return 0, false
		})
		mustPanic(t, "a second MaxAttemptsFrom", func() {
			riverx.MaxAttemptsFrom(app, func(context.Context, string) (int, bool) { return 0, false })
		})
		return nil
	})
	ctx := context.Background()

	res, err := queue.Insert(ctx, MailArgs{To: "a"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Job.MaxAttempts != 7 || res.Job.Queue != "mail" || strings.Join(res.Job.Tags, ",") != "notify" {
		t.Errorf("Insert: attempts %d, queue %q, tags %v; want 7 and the rest of the job's options", res.Job.MaxAttempts, res.Job.Queue, res.Job.Tags)
	}

	// A changed setting applies to the next insert, without a restart.
	attempts.Store(9)
	if res, err = queue.Insert(ctx, MailArgs{To: "b"}, nil); err != nil || res.Job.MaxAttempts != 9 {
		t.Errorf("after the change: %d, %v", res.Job.MaxAttempts, err)
	}

	if res, err = queue.Insert(ctx, MailArgs{To: "c"}, &river.InsertOpts{MaxAttempts: 3}); err != nil || res.Job.MaxAttempts != 3 {
		t.Errorf("explicit attempts: %d, %v", res.Job.MaxAttempts, err)
	}
	if res, err = queue.Insert(ctx, MailArgs{To: "d"}, &river.InsertOpts{Priority: 2}); err != nil || res.Job.MaxAttempts != 9 || res.Job.Priority != 2 {
		t.Errorf("explicit options without attempts: %d, priority %d, %v", res.Job.MaxAttempts, res.Job.Priority, err)
	}

	at, err := queue.InsertAt(ctx, MailArgs{To: "e"}, time.Now().Add(time.Hour))
	if err != nil || at.Job.MaxAttempts != 9 || at.Job.Queue != "mail" {
		t.Errorf("InsertAt: %+v, %v", at, err)
	}
	many, err := queue.InsertMany(ctx, MailArgs{To: "f"}, PingArgs{Text: "g"})
	if err != nil || len(many) != 2 || many[0].Job.MaxAttempts != 9 || many[1].Job.MaxAttempts != river.MaxAttemptsDefault {
		t.Errorf("InsertMany: %v", err)
	}

	pool := platform.Get[*pgxpool.Pool](<-r.app)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if res, err = queue.InsertTx(ctx, tx, MailArgs{To: "h"}, nil); err != nil || res.Job.MaxAttempts != 9 {
		t.Errorf("InsertTx: %d, %v", res.Job.MaxAttempts, err)
	}
	if res, err = queue.Insert(ctx, TickArgs{}, nil); err != nil || res.Job.MaxAttempts != river.MaxAttemptsDefault {
		t.Errorf("a zero setting: %d, %v", res.Job.MaxAttempts, err)
	}
	if res, err = queue.Insert(ctx, PingArgs{Text: "i"}, nil); err != nil || res.Job.MaxAttempts != river.MaxAttemptsDefault {
		t.Errorf("a kind the function does not manage: %d, %v", res.Job.MaxAttempts, err)
	}
}

// Periodic jobs get their attempts from MaxAttemptsFrom too, unless PeriodicJob.Opts
// sets them.
func TestMaxAttemptsFromPeriodic(t *testing.T) {
	dsn := database(t)
	ticks := &atomic.Int64{}
	r := run(t, []platform.Module{
		postgres.New(postgres.Config{URL: dsn}),
		riverx.New(riverx.Config{Work: true}),
	}, func(app *platform.App) error {
		riverx.AddWorker(app, &tickWorker{ticks: ticks})
		riverx.AddWorker(app, &pingWorker{seen: make(chan string, 8)})
		riverx.MaxAttemptsFrom(app, func(context.Context, string) (int, bool) { return 4, true })
		riverx.Periodic(app, riverx.PeriodicJob{
			Name: "tick", Schedule: func() string { return "@every 1h" }, RunOnStart: true,
			Args: func() river.JobArgs { return TickArgs{} },
		})
		riverx.Periodic(app, riverx.PeriodicJob{
			Name: "ping", Schedule: func() string { return "@every 1h" }, RunOnStart: true,
			Args: func() river.JobArgs { return PingArgs{Text: "periodic"} },
			Opts: func() *river.InsertOpts { return &river.InsertOpts{MaxAttempts: 2} },
		})
		return nil
	})
	pool := platform.Get[*pgxpool.Pool](<-r.app)

	attempts := map[string]int{}
	waitFor(t, func() bool {
		rows, err := pool.Query(context.Background(), "SELECT kind, max_attempts FROM river_job")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var kind string
			var n int
			if err := rows.Scan(&kind, &n); err != nil {
				t.Fatal(err)
			}
			attempts[kind] = n
		}
		return len(attempts) == 2
	}, "the periodic jobs")
	if attempts["tick"] != 4 || attempts["ping"] != 2 {
		t.Errorf("attempts = %v, want tick 4 from the function and ping 2 from Opts", attempts)
	}
}

// blockWorker runs until its context is cancelled, like a job stuck on a slow
// dependency.
type blockWorker struct {
	river.WorkerDefaults[TickArgs]
	running   chan struct{}
	cancelled chan struct{}
}

func (w *blockWorker) Work(ctx context.Context, _ *river.Job[TickArgs]) error {
	close(w.running)
	<-ctx.Done()
	close(w.cancelled)
	return ctx.Err()
}

// RIVER_STOP_TIMEOUT bounds how long shutdown waits for a stuck job: past it the job's
// context is cancelled and the service exits long before SHUTDOWN_TIMEOUT.
func TestStopTimeoutCancelsStuckJobs(t *testing.T) {
	dsn := database(t)
	w := &blockWorker{running: make(chan struct{}), cancelled: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- platform.RunContext(ctx, platform.Config{
			Service: "river-test", OpsAddr: "127.0.0.1:0", ShutdownTimeout: 20 * time.Second,
			Logger: logx.New(logx.Options{Writer: discard{}}),
		}, []platform.Module{
			postgres.New(postgres.Config{URL: dsn}),
			riverx.New(riverx.Config{Work: true, JobTimeout: time.Minute, StopTimeout: 300 * time.Millisecond, CancelTimeout: 5 * time.Second}),
		}, func(app *platform.App) error {
			riverx.AddWorker(app, w)
			riverx.AtStart(app, func(ctx context.Context, q *riverx.Queue) error {
				_, err := q.Insert(ctx, TickArgs{}, nil)
				return err
			})
			return nil
		})
	}()
	select {
	case <-w.running:
	case err := <-errCh:
		t.Fatalf("Run: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the job did not start")
	}

	begin := time.Now()
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return")
	}
	select {
	case <-w.cancelled:
	default:
		t.Error("the stuck job was not cancelled")
	}
	if took := time.Since(begin); took > 5*time.Second {
		t.Errorf("shutdown took %s with a 300ms stop budget", took)
	}
}
