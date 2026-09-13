package riverx_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/logx"
	"github.com/aidarbn/platform-go/kit/modules/postgres"
	"github.com/aidarbn/platform-go/kit/modules/riverx"
	"github.com/aidarbn/platform-go/kit/modules/settings"
	"github.com/aidarbn/platform-go/kit/pgdb"
	"github.com/aidarbn/platform-go/kit/platform"
	"github.com/aidarbn/platform-go/kit/settingsx"
)

func TestParseQueues(t *testing.T) {
	got, err := riverx.ParseQueues(" default=10, notify = 3 ,")
	if err != nil || got["default"] != 10 || got["notify"] != 3 || len(got) != 2 {
		t.Fatalf("ParseQueues = %v, %v", got, err)
	}
	for _, bad := range []string{"", "default", "default=0", "=5", "default=many"} {
		if _, err := riverx.ParseQueues(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestLoad(t *testing.T) {
	l := confx.New("")
	cfg := riverx.Load(l)
	if err := l.Err(); err != nil || cfg.Queues["default"] != 10 || !cfg.Work || cfg.JobTimeout != time.Minute {
		t.Fatalf("defaults: %+v, %v", cfg, err)
	}

	t.Setenv("RIVER_QUEUES", "default=2,mail=1")
	t.Setenv("RIVER_WORK", "false")
	l = confx.New("")
	cfg = riverx.Load(l)
	if err := l.Err(); err != nil || cfg.Queues["mail"] != 1 || cfg.Work {
		t.Fatalf("values: %+v, %v", cfg, err)
	}

	// A bad queue list is reported together with the rest of the configuration.
	t.Setenv("RIVER_QUEUES", "default=lots")
	l = confx.New("")
	riverx.Load(l)
	if err := l.Err(); err == nil || !strings.Contains(err.Error(), "RIVER_QUEUES") {
		t.Fatalf("err = %v", err)
	}
}

func TestInitNeedsDatabase(t *testing.T) {
	err := riverx.New(riverx.Config{}).Init(context.Background(), platform.NewApp(nil))
	if err == nil || !strings.Contains(err.Error(), "postgres module") {
		t.Fatalf("err = %v", err)
	}
}

func TestPeriodicNeedsFields(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a periodic job without a schedule was accepted")
		}
	}()
	riverx.Periodic(platform.NewApp(nil), riverx.PeriodicJob{Name: "x"})
}

// --- against a real database -------------------------------------------------------

type PingArgs struct {
	Text string `json:"text"`
}

func (PingArgs) Kind() string { return "ping" }

type pingWorker struct {
	river.WorkerDefaults[PingArgs]
	seen chan string
}

func (w *pingWorker) Work(_ context.Context, job *river.Job[PingArgs]) error {
	if job.Args.Text == "fail" {
		return river.JobCancel(errors.New("asked to fail"))
	}
	w.seen <- job.Args.Text
	return nil
}

// MailArgs carries its options the way taply's jobs do.
type MailArgs struct {
	To string `json:"to"`
}

func (MailArgs) Kind() string { return "mail" }

func (MailArgs) InsertOpts() *river.InsertOpts {
	return &river.InsertOpts{Queue: "mail", MaxAttempts: 2, Tags: []string{"notify"}}
}

type mailWorker struct {
	river.WorkerDefaults[MailArgs]
	sent chan string
}

func (w *mailWorker) Work(_ context.Context, job *river.Job[MailArgs]) error {
	w.sent <- job.Queue + ":" + job.Args.To
	return nil
}

func TestJobOptionsFromArgs(t *testing.T) {
	dsn := database(t)
	mail := &mailWorker{sent: make(chan string, 8)}
	started := make(chan string, 1)

	var queue *riverx.Queue
	run(t, []platform.Module{
		postgres.New(postgres.Config{URL: dsn}),
		riverx.New(riverx.Config{Work: true, Queues: map[string]int{"default": 2, "mail": 1}}),
	}, func(app *platform.App) error {
		riverx.AddWorker(app, mail)
		queue = riverx.QueueFrom(app)
		// A job queued on every start, like taply's bootstrap job.
		riverx.AtStart(app, func(ctx context.Context, q *riverx.Queue) error {
			_, err := q.Insert(ctx, MailArgs{To: "boot"}, nil)
			started <- "hook ran"
			return err
		})
		return nil
	})
	ctx := context.Background()

	res, err := queue.Insert(ctx, MailArgs{To: "one"}, nil)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if res.Job.Queue != "mail" || res.Job.MaxAttempts != 2 {
		t.Errorf("the options of the job were not used: queue %q, attempts %d", res.Job.Queue, res.Job.MaxAttempts)
	}

	many, err := queue.InsertMany(ctx, MailArgs{To: "two"}, MailArgs{To: "three"})
	if err != nil || len(many) != 2 || many[0].Job.Queue != "mail" {
		t.Fatalf("InsertMany = %v, %v", many, err)
	}

	later, err := queue.InsertAt(ctx, MailArgs{To: "later"}, time.Now().Add(time.Hour))
	if err != nil || later.Job.Queue != "mail" || later.Job.ScheduledAt.Before(time.Now().Add(50*time.Minute)) {
		t.Fatalf("InsertAt = %+v, %v", later, err)
	}

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the start hook did not run")
	}

	got := map[string]bool{}
	deadline := time.After(20 * time.Second)
	for len(got) < 4 {
		select {
		case s := <-mail.sent:
			got[s] = true
		case <-deadline:
			t.Fatalf("sent: %v", got)
		}
	}
	for _, want := range []string{"mail:boot", "mail:one", "mail:two", "mail:three"} {
		if !got[want] {
			t.Errorf("%s was not sent: %v", want, got)
		}
	}
	if got["mail:later"] {
		t.Error("a job scheduled for later ran now")
	}
}

type TickArgs struct{}

func (TickArgs) Kind() string { return "tick" }

type tickWorker struct {
	river.WorkerDefaults[TickArgs]
	ticks *atomic.Int64
}

func (w *tickWorker) Work(context.Context, *river.Job[TickArgs]) error {
	w.ticks.Add(1)
	return nil
}

// database creates an empty database for one test: River keeps jobs between runs, and
// a leftover job would make the next run pass or fail by accident.
func database(t *testing.T) string {
	t.Helper()
	base := os.Getenv("DATABASE_TEST_URL")
	if base == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgdb.Open(ctx, pgdb.Config{URL: base})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	name := fmt.Sprintf("river_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		admin.Close()
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

type running struct {
	app    chan *platform.App
	ops    string
	cancel context.CancelFunc
	errCh  chan error
}

func run(t *testing.T, modules []platform.Module, wire platform.Wire) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{app: make(chan *platform.App, 1), cancel: cancel, errCh: make(chan error, 1)}
	started := make(chan string, 1)
	cfg := platform.Config{
		Service: "river-test", OpsAddr: "127.0.0.1:0", ShutdownTimeout: 5 * time.Second,
		Logger:    logx.New(logx.Options{Writer: io.Discard}),
		OnStarted: func(addr string) { started <- addr },
	}
	go func() {
		r.errCh <- platform.RunContext(ctx, cfg, modules, func(app *platform.App) error {
			r.app <- app
			if wire != nil {
				return wire(app)
			}
			return nil
		})
	}()
	select {
	case r.ops = <-started:
	case err := <-r.errCh:
		t.Fatalf("Run returned before startup: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the service did not start")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-r.errCh:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("Run did not return")
		}
	})
	return r
}

func TestWorkerProcessesJobs(t *testing.T) {
	dsn := database(t)
	worker := &pingWorker{seen: make(chan string, 8)}

	var queue *riverx.Queue
	r := run(t, []platform.Module{postgres.New(postgres.Config{URL: dsn}), riverx.New(riverx.Config{Work: true, JobTimeout: 10 * time.Second})},
		func(app *platform.App) error {
			riverx.AddWorker(app, worker)
			queue = riverx.QueueFrom(app)
			// Before the modules start the queue refuses inserts instead of losing them.
			if _, err := queue.Insert(context.Background(), PingArgs{Text: "early"}, nil); !errors.Is(err, riverx.ErrNotStarted) {
				return fmt.Errorf("early insert: %v", err)
			}
			return nil
		})
	app := <-r.app
	ctx := context.Background()

	if _, err := queue.Insert(ctx, PingArgs{Text: "hello"}, nil); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// A job inserted in a transaction that rolls back never runs; a committed one does.
	pool := postgres.Pool(app)
	for _, commit := range []bool{false, true} {
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			text := "rolled back"
			if commit {
				text = "committed"
			}
			if _, err := queue.InsertTx(ctx, tx, PingArgs{Text: text}, nil); err != nil {
				return err
			}
			if !commit {
				return errors.New("roll back")
			}
			return nil
		})
		if commit && err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	if _, err := queue.Insert(ctx, PingArgs{Text: "fail"}, nil); err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	deadline := time.After(20 * time.Second)
	for len(got) < 2 {
		select {
		case text := <-worker.seen:
			got[text] = true
		case <-deadline:
			t.Fatalf("jobs seen: %v", got)
		}
	}
	if !got["hello"] || !got["committed"] {
		t.Errorf("jobs seen: %v", got)
	}
	select {
	case text := <-worker.seen:
		t.Errorf("an unexpected job ran: %q", text)
	case <-time.After(500 * time.Millisecond):
	}

	waitFor(t, func() bool {
		m := scrape(t, r.ops)
		return strings.Contains(m, `river_jobs_total{kind="ping",outcome="completed"} 2`) &&
			strings.Contains(m, `river_jobs_total{kind="ping",outcome="cancelled"} 1`)
	}, "job metrics")
}

func TestInsertOnly(t *testing.T) {
	dsn := database(t)
	worker := &pingWorker{seen: make(chan string, 1)}

	var queue *riverx.Queue
	run(t, []platform.Module{postgres.New(postgres.Config{URL: dsn}), riverx.New(riverx.Config{Work: false})},
		func(app *platform.App) error {
			riverx.AddWorker(app, worker)
			queue = riverx.QueueFrom(app)
			return nil
		})

	if _, err := queue.Insert(context.Background(), PingArgs{Text: "for another instance"}, nil); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	select {
	case text := <-worker.seen:
		t.Fatalf("an insert only instance worked a job: %q", text)
	case <-time.After(2 * time.Second):
	}
}

// The schedule and the switch of a periodic job come from business settings: changing
// them in the admin panel reschedules the job without a restart.
func TestPeriodicJobFollowsSettings(t *testing.T) {
	dsn := database(t)
	ticks := &atomic.Int64{}

	schema := settingsx.MustSchema(
		settingsx.Definition{Key: "jobs.tick.enabled", Group: "jobs.tick", Name: "enabled", Kind: settingsx.KindBool, Default: "false"},
		settingsx.Definition{Key: "jobs.tick.schedule", Group: "jobs.tick", Name: "schedule", Kind: settingsx.KindCron, Default: "@every 1s"},
	)
	rv := riverx.New(riverx.Config{Work: true})
	r := run(t, []platform.Module{
		postgres.New(postgres.Config{URL: dsn}),
		settings.New(settings.Config{RefreshInterval: 0}, schema),
		rv,
	}, func(app *platform.App) error {
		s := settingsx.From(app)
		riverx.AddWorker(app, &tickWorker{ticks: ticks})
		riverx.Periodic(app, riverx.PeriodicJob{
			Name:     "tick",
			Schedule: func() string { return s.Cron("jobs.tick.schedule") },
			Enabled:  func() bool { return s.Bool("jobs.tick.enabled") },
			Args:     func() river.JobArgs { return TickArgs{} },
		})
		return nil
	})
	app := <-r.app
	store := settingsx.From(app)
	ctx := context.Background()

	if len(rv.Scheduled()) != 0 {
		t.Fatalf("a disabled job is scheduled: %v", rv.Scheduled())
	}

	if err := store.Set(ctx, "jobs.tick.enabled", "true", "test"); err != nil {
		t.Fatal(err)
	}
	if got := rv.Scheduled()["tick"]; got != "@every 1s" {
		t.Fatalf("scheduled = %v", rv.Scheduled())
	}
	// River runs periodic jobs on the elected leader, which takes a few seconds.
	waitFor(t, func() bool { return ticks.Load() >= 2 }, "periodic runs")

	if err := store.Set(ctx, "jobs.tick.schedule", "@every 1h", "test"); err != nil {
		t.Fatal(err)
	}
	if got := rv.Scheduled()["tick"]; got != "@every 1h" {
		t.Fatalf("the new schedule did not take effect: %v", rv.Scheduled())
	}

	if err := store.Set(ctx, "jobs.tick.enabled", "false", "test"); err != nil {
		t.Fatal(err)
	}
	if len(rv.Scheduled()) != 0 {
		t.Fatalf("a disabled job stayed scheduled: %v", rv.Scheduled())
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func scrape(t *testing.T, ops string) string {
	t.Helper()
	resp, err := http.Get("http://" + ops + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return string(raw)
}
