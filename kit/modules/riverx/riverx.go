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
	"maps"
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
	// Set in the environment (RIVER_QUEUES) or in code, it wins over QueuesFrom.
	Queues map[string]int

	// Work turns job processing on. With it off the instance only inserts jobs, which
	// is how an API instance and a worker instance of one binary split the load.
	Work bool

	JobTimeout time.Duration // how long one job may run

	// How long finished jobs stay in the database before River deletes them.
	CompletedRetention time.Duration
	CancelledRetention time.Duration
	DiscardedRetention time.Duration

	// Shutdown budget of the queue. StopTimeout is how long running jobs may finish on
	// their own; CancelTimeout is how long they then get to return once their context
	// is cancelled. Zero StopTimeout waits as long as the shutdown timeout allows, zero
	// CancelTimeout waits for the cancelled jobs without a limit.
	StopTimeout   time.Duration
	CancelTimeout time.Duration

	// queuesDefault marks Queues that Load filled in because RIVER_QUEUES is unset, so
	// QueuesFrom may replace them.
	queuesDefault bool
}

// Load reads the module settings from environment variables.
func Load(l *confx.Loader) Config {
	cfg := Config{
		Work:               l.Bool("RIVER_WORK", true),
		JobTimeout:         l.Duration("RIVER_JOB_TIMEOUT", time.Minute),
		CompletedRetention: l.Duration("RIVER_COMPLETED_RETENTION", 24*time.Hour),
		CancelledRetention: l.Duration("RIVER_CANCELLED_RETENTION", 24*time.Hour),
		DiscardedRetention: l.Duration("RIVER_DISCARDED_RETENTION", 7*24*time.Hour),
		StopTimeout:        l.Duration("RIVER_STOP_TIMEOUT", 0),
		CancelTimeout:      l.Duration("RIVER_CANCEL_TIMEOUT", 0),
	}
	for _, b := range []struct {
		name string
		d    time.Duration
	}{{"RIVER_STOP_TIMEOUT", cfg.StopTimeout}, {"RIVER_CANCEL_TIMEOUT", cfg.CancelTimeout}} {
		if b.d < 0 {
			l.Fail(fmt.Errorf("%s: %s is negative", l.Key(b.name), b.d))
		}
	}
	raw := l.String("RIVER_QUEUES", "")
	cfg.queuesDefault = raw == ""
	if cfg.queuesDefault {
		raw = defaultQueues
	}
	queues, err := ParseQueues(raw)
	l.Fail(err)
	cfg.Queues = queues
	return cfg
}

const defaultQueues = "default=10"

// ParseQueues reads "default=10,notify=3".
func ParseQueues(raw string) (map[string]int, error) {
	return parseQueues("RIVER_QUEUES", raw)
}

func parseQueues(source, raw string) (map[string]int, error) {
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
			return nil, fmt.Errorf("%s: %q: expected queue=workers, for example default=10", source, part)
		}
		out[name] = n
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no queues", source)
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
	queues   func(context.Context) (map[string]int, error)
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

// QueuesFrom takes the queues and their capacity from the application, typically from
// business settings, as taply sizes its queues from app_configs. fn is called once, when
// the module starts and the settings are loaded; a changed value applies after a
// restart, so mark such settings requires_restart. Queues set in RIVER_QUEUES or in
// Config win over fn; without either the module works default=10.
//
//	riverx.QueuesFrom(app, func(context.Context) (map[string]int, error) {
//		return map[string]int{"sync": s.Sync().QueueWorkers(), "payments": 3}, nil
//	})
func QueuesFrom(app *platform.App, fn func(ctx context.Context) (map[string]int, error)) {
	if fn == nil {
		panic("riverx: QueuesFrom needs a function")
	}
	r := platform.Get[*Registry](app)
	r.add(func() {
		if r.queues != nil {
			panic("riverx: QueuesFrom is called twice")
		}
		r.queues = fn
	})
}

// MaxAttemptsFrom takes the number of attempts of a job kind from the application at
// insert time, as taply reads create_order.max_attempts: a changed value applies to the
// jobs inserted after the change. fn reports false for kinds it does not manage.
//
// The value replaces the attempts the job carries in its InsertOpts; attempts passed
// explicitly to Insert or InsertTx, or returned by PeriodicJob.Opts, win over it. It
// applies to every insert of Queue and to periodic jobs, not to inserts made through
// Client(), which is River as it is.
//
//	riverx.MaxAttemptsFrom(app, func(_ context.Context, kind string) (int, bool) {
//		if kind == (jobs.CreateOrderArgs{}).Kind() {
//			return s.CreateOrder().MaxAttempts(), true
//		}
//		return 0, false
//	})
func MaxAttemptsFrom(app *platform.App, fn func(ctx context.Context, kind string) (int, bool)) {
	if fn == nil {
		panic("riverx: MaxAttemptsFrom needs a function")
	}
	r := platform.Get[*Registry](app)
	q := platform.Get[*Queue](app)
	r.add(func() {
		q.mu.Lock()
		defer q.mu.Unlock()
		if q.maxAttempts != nil {
			panic("riverx: MaxAttemptsFrom is called twice")
		}
		q.maxAttempts = &attemptsFrom{fn: fn}
	})
}

// Queue inserts jobs. It is available from wireDomain on, before the client starts,
// so domain services can keep it; inserting before Start returns an error.
type Queue struct {
	mu          sync.RWMutex
	client      *river.Client[pgx.Tx]
	maxAttempts *attemptsFrom // behind a pointer, so Queue stays comparable
}

type attemptsFrom struct {
	fn func(context.Context, string) (int, bool)
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

// opts returns the options of one insert: the ones passed explicitly or carried by the
// job, with the attempts of MaxAttemptsFrom unless the explicit options set their own.
func (q *Queue) opts(ctx context.Context, args river.JobArgs, explicit *river.InsertOpts) *river.InsertOpts {
	opts := optsFor(args, explicit)
	q.mu.RLock()
	from := q.maxAttempts
	q.mu.RUnlock()
	if from == nil || (explicit != nil && explicit.MaxAttempts > 0) {
		return opts
	}
	n, ok := from.fn(ctx, args.Kind())
	if !ok || n <= 0 {
		return opts
	}
	out := river.InsertOpts{}
	if opts != nil {
		out = *opts
	}
	out.MaxAttempts = n
	return &out
}

// Insert adds a job.
func (q *Queue) Insert(ctx context.Context, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	c, err := q.get()
	if err != nil {
		return nil, err
	}
	return c.Insert(ctx, args, q.opts(ctx, args, opts))
}

// InsertAt adds a job that runs no earlier than at, keeping the options the job carries.
func (q *Queue) InsertAt(ctx context.Context, args river.JobArgs, at time.Time) (*rivertype.JobInsertResult, error) {
	c, err := q.get()
	if err != nil {
		return nil, err
	}
	opts := river.InsertOpts{}
	if base := q.opts(ctx, args, nil); base != nil {
		opts = *base
	}
	opts.ScheduledAt = at
	return c.Insert(ctx, args, &opts)
}

// InsertMany adds several jobs in one round trip, each with the options it carries.
func (q *Queue) InsertMany(ctx context.Context, args ...river.JobArgs) ([]*rivertype.JobInsertResult, error) {
	c, err := q.get()
	if err != nil {
		return nil, err
	}
	params := make([]river.InsertManyParams, len(args))
	for i, a := range args {
		params[i] = river.InsertManyParams{Args: a, InsertOpts: q.opts(ctx, a, nil)}
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
	return c.InsertTx(ctx, tx, args, q.opts(ctx, args, opts))
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
		cfg.queuesDefault = true
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
	workerCount, periodic, atStart, queuesFn := m.registry.count, m.registry.periodic, m.registry.atStart, m.registry.queues
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
	queues := m.cfg.Queues
	if working {
		var err error
		if queues, err = m.queues(ctx, queuesFn); err != nil {
			return err
		}
		cfg.Workers = m.registry.workers
		cfg.Queues = make(map[string]river.QueueConfig, len(queues))
		for name, n := range queues {
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
	m.log.Info("river is working", "queues", queues, "workers", workerCount, "periodic", len(periodic))
	m.runAtStart(ctx, atStart)
	return nil
}

// queues picks the queues to work: RIVER_QUEUES or Config, then QueuesFrom, then the
// default.
func (m *Module) queues(ctx context.Context, fn func(context.Context) (map[string]int, error)) (map[string]int, error) {
	// Queues still default unless code replaced what Load filled in.
	isDefault := m.cfg.queuesDefault && maps.Equal(m.cfg.Queues, map[string]int{river.QueueDefault: 10})
	if !isDefault || fn == nil {
		return m.cfg.Queues, nil
	}
	got, err := fn(ctx)
	if err != nil {
		return nil, fmt.Errorf("river queues: %w", err)
	}
	if len(got) == 0 {
		return nil, errors.New("river queues: QueuesFrom returned no queues")
	}
	for name, n := range got {
		if strings.TrimSpace(name) == "" || n <= 0 {
			return nil, fmt.Errorf("river queues: QueuesFrom: %q=%d: expected a name and a positive number of workers", name, n)
		}
	}
	return got, nil
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
			return args, m.queue.opts(context.Background(), args, o)
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

// Stop lets running jobs finish within RIVER_STOP_TIMEOUT and the shutdown timeout,
// then cancels them and waits up to RIVER_CANCEL_TIMEOUT for them to return.
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
	return stopClient(ctx, m.client, m.cfg.StopTimeout, m.cfg.CancelTimeout, m.log)
}

// StopPhases puts the queue between the HTTP and the gRPC servers of a phased shutdown.
func (m *Module) StopPhases() []platform.StopPhase {
	return []platform.StopPhase{{Name: "queue", Order: platform.PhaseQueue, Stop: m.Stop}}
}

type stopper interface {
	Stop(ctx context.Context) error
	StopAndCancel(ctx context.Context) error
}

// stopClient is the shutdown of taply: a soft stop waits for the running jobs, and the
// jobs that do not finish in time get their context cancelled and the rest of the
// budget to return, so none of them writes into a database pool that is closed.
func stopClient(ctx context.Context, c stopper, soft, hard time.Duration, log *slog.Logger) error {
	softCtx := ctx
	if soft > 0 {
		var cancel context.CancelFunc
		softCtx, cancel = context.WithTimeout(ctx, soft)
		defer cancel()
	}
	err := c.Stop(softCtx)
	if err == nil {
		return nil
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	log.Warn("river jobs did not finish in time, cancelling them")

	hardCtx := context.WithoutCancel(ctx)
	if hard > 0 {
		var cancel context.CancelFunc
		hardCtx, cancel = context.WithTimeout(hardCtx, hard)
		defer cancel()
	}
	return c.StopAndCancel(hardCtx)
}
