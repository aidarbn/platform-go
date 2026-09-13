// Package riverx is a platform module: background jobs on River, stored in the
// PostgreSQL database of the postgres module.
//
// The project declares its workers and periodic jobs from wireDomain and inserts jobs
// through Queue. The module migrates River's tables, runs the client, reports job
// metrics, and re-reads schedules taken from business settings without a restart.
package riverx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"
	"github.com/robfig/cron/v3"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/platform"
	"github.com/aidarbn/platform-go/kit/settingsx"
)

// Config holds module settings. Load fills it from the environment; the platform
// generator writes the Load call into the project's config.gen.go.
type Config struct {
	// Queues maps a queue to the number of jobs it works at once. Capacity is an
	// environment parameter: production and staging differ in it, the jobs do not.
	Queues map[string]int

	// Work turns job processing on. With it off the instance only inserts jobs, which
	// is how an API instance and a worker instance of one binary split the load.
	Work bool

	JobTimeout time.Duration // how long one job may run

	// How long finished jobs stay in the database before River deletes them.
	CompletedRetention time.Duration
	CancelledRetention time.Duration
	DiscardedRetention time.Duration
}

// Load reads the module settings from environment variables.
func Load(l *confx.Loader) Config {
	cfg := Config{
		Work:               l.Bool("RIVER_WORK", true),
		JobTimeout:         l.Duration("RIVER_JOB_TIMEOUT", time.Minute),
		CompletedRetention: l.Duration("RIVER_COMPLETED_RETENTION", 24*time.Hour),
		CancelledRetention: l.Duration("RIVER_CANCELLED_RETENTION", 24*time.Hour),
		DiscardedRetention: l.Duration("RIVER_DISCARDED_RETENTION", 7*24*time.Hour),
	}
	queues, err := ParseQueues(l.String("RIVER_QUEUES", "default=10"))
	l.Fail(err)
	cfg.Queues = queues
	return cfg
}

// ParseQueues reads "default=10,notify=3".
func ParseQueues(raw string) (map[string]int, error) {
	out := map[string]int{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, count, ok := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		n, err := strconv.Atoi(strings.TrimSpace(count))
		if !ok || name == "" || err != nil || n <= 0 {
			return nil, fmt.Errorf("RIVER_QUEUES: %q: expected queue=workers, for example default=10", part)
		}
		out[name] = n
	}
	if len(out) == 0 {
		return nil, errors.New("RIVER_QUEUES: no queues")
	}
	return out, nil
}

// PeriodicJob is a job River inserts on a schedule. Schedule and Enabled are functions,
// so they can come from business settings: a changed value takes effect without a
// restart.
//
//	riverx.Periodic(app, riverx.PeriodicJob{
//		Name:     "orders.cleanup",
//		Schedule: s.OrdersCleanup().Schedule,
//		Enabled:  s.OrdersCleanup().Enabled,
//		Args:     func() river.JobArgs { return jobs.CleanupArgs{} },
//	})
type PeriodicJob struct {
	Name       string                   // unique name, used for the schedule and in logs
	Schedule   func() string            // cron expression, @daily, @every 15m
	Enabled    func() bool              // nil means always enabled
	Args       func() river.JobArgs     // the job to insert each time
	Opts       func() *river.InsertOpts // nil means the options the job carries
	RunOnStart bool
}

// ConfigurableArgs is a job that carries its own insert options: queue, priority,
// attempts, uniqueness. It is the convention of taply — the options live next to the
// job kind, so every place that inserts the job gets the same ones.
//
//	func (OrderCleanupArgs) InsertOpts() *river.InsertOpts {
//		return &river.InsertOpts{Queue: "maintenance", MaxAttempts: 1}
//	}
type ConfigurableArgs interface {
	river.JobArgs
	InsertOpts() *river.InsertOpts
}

// optsFor returns the options passed explicitly, or the ones the job carries.
func optsFor(args river.JobArgs, opts *river.InsertOpts) *river.InsertOpts {
	if opts != nil {
		return opts
	}
	if c, ok := args.(ConfigurableArgs); ok {
		return c.InsertOpts()
	}
	return nil
}

// Registry holds what the project declares. It is filled from wireDomain.
type Registry struct {
	mu       sync.Mutex
	sealed   bool
	workers  *river.Workers
	count    int
	periodic []PeriodicJob
	atStart  []func(context.Context, *Queue) error
}

func (r *Registry) add(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		panic("riverx: add workers and periodic jobs in wireDomain, before the modules start")
	}
	fn()
}

// AddWorker registers the worker of one job kind.
func AddWorker[T river.JobArgs](app *platform.App, w river.Worker[T]) {
	r := platform.Get[*Registry](app)
	r.add(func() {
		river.AddWorker(r.workers, w)
		r.count++
	})
}

// Periodic registers a periodic job.
func Periodic(app *platform.App, job PeriodicJob) {
	if job.Name == "" || job.Schedule == nil || job.Args == nil {
		panic("riverx: a periodic job needs a name, a schedule and args")
	}
	r := platform.Get[*Registry](app)
	r.add(func() {
		for _, existing := range r.periodic {
			if existing.Name == job.Name {
				panic("riverx: duplicate periodic job " + job.Name)
			}
		}
		r.periodic = append(r.periodic, job)
	})
}

// AtStart runs fn once the queue has started: the place for a job that must be queued on
// every start, such as a bootstrap job. A failure is logged and does not stop the
// service.
func AtStart(app *platform.App, fn func(ctx context.Context, q *Queue) error) {
	r := platform.Get[*Registry](app)
	r.add(func() { r.atStart = append(r.atStart, fn) })
}

// Queue inserts jobs. It is available from wireDomain on, before the client starts,
// so domain services can keep it; inserting before Start returns an error.
type Queue struct {
	mu     sync.RWMutex
	client *river.Client[pgx.Tx]
}

// ErrNotStarted is returned when a job is inserted before the module has started.
var ErrNotStarted = errors.New("riverx: the job queue is not started yet")

func (q *Queue) get() (*river.Client[pgx.Tx], error) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.client == nil {
		return nil, ErrNotStarted
	}
	return q.client, nil
}

// Insert adds a job.
func (q *Queue) Insert(ctx context.Context, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	c, err := q.get()
	if err != nil {
		return nil, err
	}
	return c.Insert(ctx, args, optsFor(args, opts))
}

// InsertAt adds a job that runs no earlier than at, keeping the options the job carries.
func (q *Queue) InsertAt(ctx context.Context, args river.JobArgs, at time.Time) (*rivertype.JobInsertResult, error) {
	opts := river.InsertOpts{}
	if base := optsFor(args, nil); base != nil {
		opts = *base
	}
	opts.ScheduledAt = at
	return q.Insert(ctx, args, &opts)
}

// InsertMany adds several jobs in one round trip, each with the options it carries.
func (q *Queue) InsertMany(ctx context.Context, args ...river.JobArgs) ([]*rivertype.JobInsertResult, error) {
	c, err := q.get()
	if err != nil {
		return nil, err
	}
	params := make([]river.InsertManyParams, len(args))
	for i, a := range args {
		params[i] = river.InsertManyParams{Args: a, InsertOpts: optsFor(a, nil)}
	}
	return c.InsertMany(ctx, params)
}

// InsertTx adds a job inside the transaction of the domain change it belongs to: the
// job exists exactly when the change is committed.
func (q *Queue) InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	c, err := q.get()
	if err != nil {
		return nil, err
	}
	return c.InsertTx(ctx, tx, args, optsFor(args, opts))
}

// Client returns the River client for what Queue does not cover. It is nil before
// Start.
func (q *Queue) Client() *river.Client[pgx.Tx] {
	c, _ := q.get()
	return c
}

// QueueFrom returns the queue from the container.
func QueueFrom(app *platform.App) *Queue { return platform.Get[*Queue](app) }

// Module implements platform.Module.
type Module struct {
	cfg      Config
	registry *Registry
	queue    *Queue
	app      *platform.App
	log      *slog.Logger
	jobs     *prometheus.CounterVec
	duration *prometheus.HistogramVec

	client      *river.Client[pgx.Tx]
	started     bool
	unsubscribe func()
	done        chan struct{}
	wg          sync.WaitGroup

	periodicMu sync.Mutex
	scheduled  map[string]scheduled
}

type scheduled struct {
	handle   rivertype.PeriodicJobHandle
	schedule string
}

// New creates the module from ready settings.
func New(cfg Config) *Module {
	if len(cfg.Queues) == 0 {
		cfg.Queues = map[string]int{river.QueueDefault: 10}
	}
	return &Module{
		cfg:       cfg,
		registry:  &Registry{workers: river.NewWorkers()},
		queue:     &Queue{},
		done:      make(chan struct{}),
		scheduled: map[string]scheduled{},
	}
}

func (m *Module) Name() string { return "river" }

// Init migrates River's tables and puts the registry and the queue into the container.
func (m *Module) Init(ctx context.Context, app *platform.App) error {
	pool, ok := platform.Lookup[*pgxpool.Pool](app)
	if !ok {
		return errors.New("no database in the container: enable the postgres module")
	}
	m.app = app
	m.log = app.Logger().With("module", "river")

	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("river migrations: %w", err)
	}
	res, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return fmt.Errorf("river migrations: %w", err)
	}
	for _, v := range res.Versions {
		m.log.Info("river migration applied", "version", v.Version)
	}

	m.jobs = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "river", Name: "jobs_total", Help: "finished jobs by kind and outcome",
	}, []string{"kind", "outcome"})
	m.duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "river", Name: "job_duration_seconds", Help: "time a job ran", Buckets: prometheus.DefBuckets,
	}, []string{"kind"})
	for _, c := range []prometheus.Collector{m.jobs, m.duration} {
		if err := app.Metrics().Register(c); err != nil {
			return fmt.Errorf("river metrics: %w", err)
		}
	}

	platform.Provide(app, m.registry)
	platform.Provide(app, m.queue)
	return nil
}

// Start creates the client from what the project declared and starts working.
func (m *Module) Start(ctx context.Context) error {
	m.registry.mu.Lock()
	m.registry.sealed = true
	workerCount, periodic, atStart := m.registry.count, m.registry.periodic, m.registry.atStart
	m.registry.mu.Unlock()

	pool := platform.Get[*pgxpool.Pool](m.app)
	// Fetch timings as in taply: a new job is picked up within a second even when the
	// notification that announces it is lost.
	cfg := &river.Config{
		Logger:                      m.log,
		JobTimeout:                  m.cfg.JobTimeout,
		FetchCooldown:               100 * time.Millisecond,
		FetchPollInterval:           time.Second,
		CompletedJobRetentionPeriod: m.cfg.CompletedRetention,
		CancelledJobRetentionPeriod: m.cfg.CancelledRetention,
		DiscardedJobRetentionPeriod: m.cfg.DiscardedRetention,
	}
	// A client without workers or with work turned off only inserts: River refuses
	// queues it has nobody to run for.
	working := m.cfg.Work && workerCount > 0 && m.app.Serves(platform.RoleWorker)
	if working {
		cfg.Workers = m.registry.workers
		cfg.Queues = make(map[string]river.QueueConfig, len(m.cfg.Queues))
		for name, n := range m.cfg.Queues {
			cfg.Queues[name] = river.QueueConfig{MaxWorkers: n}
		}
	}

	client, err := river.NewClient(riverpgxv5.New(pool), cfg)
	if err != nil {
		return fmt.Errorf("river client: %w", err)
	}
	m.client = client
	m.queue.mu.Lock()
	m.queue.client = client
	m.queue.mu.Unlock()

	if !working {
		m.log.Info("river inserts only", "work", m.cfg.Work, "workers", workerCount, "role", m.app.Role())
		m.runAtStart(ctx, atStart)
		return nil
	}

	events, cancel := client.Subscribe(river.EventKindJobCompleted, river.EventKindJobFailed, river.EventKindJobCancelled, river.EventKindJobSnoozed)
	m.unsubscribe = cancel
	m.wg.Add(1)
	go m.observe(events)

	if err := client.Start(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("river start: %w", err)
	}
	m.started = true

	for _, job := range periodic {
		m.schedule(job)
	}
	if store, ok := platform.Lookup[*settingsx.Store](m.app); ok && len(periodic) > 0 {
		// Any settings change re-reads the schedules; only jobs whose schedule or state
		// actually changed are replaced.
		store.Watch(func([]string) {
			for _, job := range periodic {
				m.schedule(job)
			}
		})
	}
	m.log.Info("river is working", "queues", m.cfg.Queues, "workers", workerCount, "periodic", len(periodic))
	m.runAtStart(ctx, atStart)
	return nil
}

func (m *Module) runAtStart(ctx context.Context, fns []func(context.Context, *Queue) error) {
	for _, fn := range fns {
		if err := fn(ctx, m.queue); err != nil {
			m.log.Error("a start hook of the queue failed", "err", err)
		}
	}
}

// schedule brings one periodic job in line with its current schedule and state.
func (m *Module) schedule(job PeriodicJob) {
	m.periodicMu.Lock()
	defer m.periodicMu.Unlock()

	enabled := job.Enabled == nil || job.Enabled()
	expr := strings.TrimSpace(job.Schedule())
	current, has := m.scheduled[job.Name]

	if has && enabled && current.schedule == expr {
		return
	}
	if has {
		m.client.PeriodicJobs().Remove(current.handle)
		delete(m.scheduled, job.Name)
		m.log.Info("periodic job unscheduled", "job", job.Name, "schedule", current.schedule)
	}
	if !enabled {
		return
	}

	sched, err := cron.ParseStandard(expr)
	if err != nil {
		m.log.Error("periodic job has a bad schedule and is not scheduled", "job", job.Name, "schedule", expr, "err", err)
		return
	}
	opts := job.Opts
	handle := m.client.PeriodicJobs().Add(river.NewPeriodicJob(
		sched,
		func() (river.JobArgs, *river.InsertOpts) {
			args := job.Args()
			var o *river.InsertOpts
			if opts != nil {
				o = opts()
			}
			return args, optsFor(args, o)
		},
		&river.PeriodicJobOpts{RunOnStart: job.RunOnStart},
	))
	m.scheduled[job.Name] = scheduled{handle: handle, schedule: expr}
	m.log.Info("periodic job scheduled", "job", job.Name, "schedule", expr)
}

func (m *Module) observe(events <-chan *river.Event) {
	defer m.wg.Done()
	for {
		select {
		case <-m.done:
			return
		case e, ok := <-events:
			if !ok {
				return
			}
			if e.Job == nil {
				continue
			}
			outcome := strings.TrimPrefix(string(e.Kind), "job_")
			m.jobs.WithLabelValues(e.Job.Kind, outcome).Inc()
			if e.JobStats != nil {
				m.duration.WithLabelValues(e.Job.Kind).Observe(e.JobStats.RunDuration.Seconds())
			}
		}
	}
}

// Scheduled returns the schedules currently in effect, by job name. The admin panel and
// tests read it.
func (m *Module) Scheduled() map[string]string {
	m.periodicMu.Lock()
	defer m.periodicMu.Unlock()
	out := make(map[string]string, len(m.scheduled))
	for name, s := range m.scheduled {
		out[name] = s.schedule
	}
	return out
}

// Health fails when the client is not running.
func (m *Module) Health(context.Context) error {
	if m.client == nil {
		return errors.New("river is not started")
	}
	return nil
}

// Stop lets running jobs finish within the shutdown timeout, then cancels them.
func (m *Module) Stop(ctx context.Context) error {
	select {
	case <-m.done:
	default:
		close(m.done)
	}
	if m.unsubscribe != nil {
		m.unsubscribe()
	}
	m.wg.Wait()
	if !m.started {
		return nil
	}
	if err := m.client.Stop(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if ctx.Err() != nil {
		return m.client.StopAndCancel(context.WithoutCancel(ctx))
	}
	return nil
}
