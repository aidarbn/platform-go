package webhookx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/aidarbn/platform-go/kit/logx"
)

// AlertDeliveryFailed is the alert kind logged when a webhook is given up on.
const AlertDeliveryFailed = "webhook_delivery_failed"

// DeliveryAttempts is how many times a webhook is tried. River's backoff (attempt⁴
// seconds) spreads them over about two days.
const DeliveryAttempts = 15

// DeliveryArgs is the River job of one webhook. It carries the endpoint by name, never
// its URL or secret, so the job table holds no credentials and a rotated secret applies
// to retries.
type DeliveryArgs struct {
	Endpoint string          `json:"endpoint"`
	ID       string          `json:"id"`
	Body     json.RawMessage `json:"body"`
}

func (DeliveryArgs) Kind() string { return "webhook_delivery" }

// InsertOpts is River's own form, so the attempts apply through riverx.Queue and a
// bare river.Client alike.
func (DeliveryArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: DeliveryAttempts}
}

// Endpoint is a receiver of the project's webhooks.
type Endpoint struct {
	URL    string
	Secret Secret
}

// SenderConfig lists the endpoints by name.
type SenderConfig struct {
	Endpoints map[string]Endpoint
	// Timeout of one attempt, 15s when zero.
	Timeout time.Duration
	Client  *http.Client // http.DefaultClient when nil
	Logger  *slog.Logger
	Now     func() time.Time // for tests
}

// Sender queues webhooks and is the River worker that delivers them: register it with
// riverx.AddWorker.
type Sender struct {
	river.WorkerDefaults[DeliveryArgs]
	cfg SenderConfig
}

// Inserter adds a job in a transaction: *riverx.Queue and *river.Client do.
type Inserter interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// NewSender checks the endpoints.
func NewSender(cfg SenderConfig) (*Sender, error) {
	names := make([]string, 0, len(cfg.Endpoints))
	for name := range cfg.Endpoints {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		e := cfg.Endpoints[name]
		u, err := url.Parse(e.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, fmt.Errorf("webhookx: endpoint %s: %q is not an http(s) URL", name, e.URL)
		}
		if len(e.Secret) == 0 {
			return nil, fmt.Errorf("webhookx: endpoint %s: no secret", name)
		}
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.Client == nil {
		cfg.Client = http.DefaultClient
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Sender{cfg: cfg}, nil
}

// message is the body of the Standard Webhooks format.
type message struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Data      any       `json:"data"`
}

// Enqueue queues a webhook of eventType with data to the endpoint inside tx: it goes out
// exactly when tx commits. It returns the webhook id, which every retry repeats so the
// receiver can drop copies.
func (s *Sender) Enqueue(ctx context.Context, q Inserter, tx pgx.Tx, endpoint, eventType string, data any) (string, error) {
	if _, ok := s.cfg.Endpoints[endpoint]; !ok {
		return "", fmt.Errorf("webhookx: unknown endpoint %q", endpoint)
	}
	if eventType == "" {
		return "", errors.New("webhookx: empty event type")
	}
	body, err := json.Marshal(message{Type: eventType, Timestamp: s.cfg.Now().UTC(), Data: data})
	if err != nil {
		return "", fmt.Errorf("webhookx: %s: %w", eventType, err)
	}
	args := DeliveryArgs{Endpoint: endpoint, ID: newID(), Body: body}
	if _, err := q.InsertTx(ctx, tx, args, nil); err != nil {
		return "", fmt.Errorf("webhookx: queue %s: %w", eventType, err)
	}
	return args.ID, nil
}

// Timeout bounds one attempt.
func (s *Sender) Timeout(*river.Job[DeliveryArgs]) time.Duration { return s.cfg.Timeout }

// Work delivers one webhook. A 2xx answer completes it, 410 Gone cancels it, anything
// else is retried; the last failed attempt logs AlertDeliveryFailed.
func (s *Sender) Work(ctx context.Context, job *river.Job[DeliveryArgs]) error {
	a := job.Args
	log := s.cfg.Logger.With("endpoint", a.Endpoint, "webhook_id", a.ID, "attempt", job.Attempt)

	e, ok := s.cfg.Endpoints[a.Endpoint]
	if !ok {
		log.ErrorContext(ctx, "webhook to an endpoint no longer configured", logx.AlertKey, AlertDeliveryFailed)
		return river.JobCancel(fmt.Errorf("webhookx: unknown endpoint %q", a.Endpoint))
	}

	err := s.post(ctx, e, a)
	var gone goneError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &gone):
		log.WarnContext(ctx, "webhook endpoint is gone", "error", err)
		return river.JobCancel(err)
	case job.Attempt >= job.MaxAttempts:
		log.ErrorContext(ctx, "webhook given up", "error", err, logx.AlertKey, AlertDeliveryFailed)
	default:
		log.WarnContext(ctx, "webhook attempt failed", "error", err)
	}
	return err
}

type goneError struct{ error }

func (s *Sender) post(ctx context.Context, e Endpoint, a DeliveryArgs) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.URL, bytes.NewReader(a.Body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "platform-go-webhooks")
	SetHeaders(req.Header, e.Secret, a.ID, s.cfg.Now(), a.Body)

	resp, err := s.cfg.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusGone:
		return goneError{fmt.Errorf("webhookx: %s answered 410 Gone", a.Endpoint)}
	default:
		return fmt.Errorf("webhookx: %s answered %d: %s", a.Endpoint, resp.StatusCode, bytes.TrimSpace(snippet))
	}
}
