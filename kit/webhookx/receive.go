package webhookx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aidarbn/platform-go/kit/pgdb"
)

// Event is a verified incoming webhook.
type Event struct {
	ID        string
	Timestamp time.Time
	Body      []byte
	Header    http.Header
}

// HandleFunc processes one webhook inside the transaction that records its id: domain
// changes and jobs inserted with tx commit together with it, and an error rolls all of
// it back, so the sender's retry is processed again.
type HandleFunc func(ctx context.Context, tx pgx.Tx, e Event) error

// ReceiverConfig describes one incoming webhook source.
type ReceiverConfig struct {
	// Source names the sender; ids are unique per source.
	Source string
	// Secrets verify the signature: the current one, and the previous one while it is rotated.
	Secrets []Secret
	DB      *pgxpool.Pool
	Handle  HandleFunc

	Tolerance time.Duration // DefaultTolerance when zero
	MaxBody   int64         // 1 MiB when zero
	// Retention keeps processed ids, 24h when zero; it must exceed twice the tolerance,
	// or a replay inside the tolerance window could be processed again.
	Retention time.Duration
	Logger    *slog.Logger
	Now       func() time.Time // for tests
}

// Receiver is the http.Handler of one webhook source. Serve it with api.HandleHTTP.
type Receiver struct {
	cfg ReceiverConfig

	mu         sync.Mutex
	schemaDone bool
	lastPrune  time.Time
}

const inboundTable = "platform_webhook_inbound"

var inboundSchema = []pgdb.SchemaStep{{Version: 1, Statements: []string{
	`CREATE TABLE ` + inboundTable + ` (
	source      text        NOT NULL,
	id          text        NOT NULL,
	received_at timestamptz NOT NULL DEFAULT now(),
	PRIMARY KEY (source, id)
)`,
	`CREATE INDEX ` + inboundTable + `_received_at_idx ON ` + inboundTable + ` (received_at)`,
}}}

// NewReceiver checks the configuration. The table of processed ids is created with the
// first webhook, so wiring needs no database round trip.
func NewReceiver(cfg ReceiverConfig) (*Receiver, error) {
	switch {
	case cfg.Source == "":
		return nil, errors.New("webhookx: receiver: empty source")
	case len(cfg.Secrets) == 0:
		return nil, fmt.Errorf("webhookx: receiver %s: no secrets", cfg.Source)
	case cfg.DB == nil:
		return nil, fmt.Errorf("webhookx: receiver %s: no database", cfg.Source)
	case cfg.Handle == nil:
		return nil, fmt.Errorf("webhookx: receiver %s: no handler", cfg.Source)
	}
	if cfg.Tolerance == 0 {
		cfg.Tolerance = DefaultTolerance
	}
	if cfg.MaxBody == 0 {
		cfg.MaxBody = 1 << 20
	}
	if cfg.Retention == 0 {
		cfg.Retention = 24 * time.Hour
	}
	if cfg.Retention <= 2*cfg.Tolerance {
		return nil, fmt.Errorf("webhookx: receiver %s: retention %s must exceed twice the tolerance %s", cfg.Source, cfg.Retention, cfg.Tolerance)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Receiver{cfg: cfg}, nil
}

// ServeHTTP answers 204 to a processed or repeated webhook, 4xx to one that must not be
// retried as it is, and 500 when the handler failed, so the sender retries.
func (r *Receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, r.cfg.MaxBody))
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	log := r.cfg.Logger.With("source", r.cfg.Source)

	id, at, err := Verify(req.Header, body, r.cfg.Secrets, r.cfg.Now(), r.cfg.Tolerance)
	switch {
	case errors.Is(err, ErrMissingHeaders), errors.Is(err, ErrBadTimestamp):
		http.Error(w, "missing or invalid webhook headers", http.StatusBadRequest)
		return
	case err != nil:
		log.WarnContext(req.Context(), "webhook rejected", "error", err)
		http.Error(w, "invalid webhook signature", http.StatusUnauthorized)
		return
	}

	ctx := req.Context()
	if err := r.ensureSchema(ctx); err != nil {
		log.ErrorContext(ctx, "webhook table", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	fresh, err := r.process(ctx, Event{ID: id, Timestamp: at, Body: body, Header: req.Header})
	if err != nil {
		log.ErrorContext(ctx, "webhook failed", "id", id, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !fresh {
		log.InfoContext(ctx, "webhook repeated", "id", id)
	}
	w.WriteHeader(http.StatusNoContent)
	r.prune(ctx, log)
}

// process records the id and runs the handler in one transaction. A concurrent copy of
// the same webhook waits on the primary key and then finds it recorded.
func (r *Receiver) process(ctx context.Context, e Event) (bool, error) {
	tx, err := r.cfg.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, "INSERT INTO "+inboundTable+" (source, id) VALUES ($1, $2) ON CONFLICT DO NOTHING",
		r.cfg.Source, e.ID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if err := r.cfg.Handle(ctx, tx, e); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (r *Receiver) ensureSchema(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.schemaDone {
		return nil
	}
	if err := pgdb.MigrateSchema(ctx, r.cfg.DB, "webhookx:inbound", inboundSchema); err != nil {
		return err
	}
	r.schemaDone = true
	return nil
}

// prune removes ids past the retention, at most once an hour per receiver.
func (r *Receiver) prune(ctx context.Context, log *slog.Logger) {
	now := r.cfg.Now()
	r.mu.Lock()
	due := now.Sub(r.lastPrune) >= time.Hour
	if due {
		r.lastPrune = now
	}
	r.mu.Unlock()
	if !due {
		return
	}
	if _, err := r.cfg.DB.Exec(ctx, "DELETE FROM "+inboundTable+" WHERE source = $1 AND received_at < $2",
		r.cfg.Source, now.Add(-r.cfg.Retention)); err != nil {
		log.WarnContext(ctx, "prune webhook ids", "error", err)
	}
}
