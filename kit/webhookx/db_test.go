package webhookx_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aidarbn/platform-go/kit/logx"
	"github.com/aidarbn/platform-go/kit/modules/postgres"
	"github.com/aidarbn/platform-go/kit/modules/riverx"
	"github.com/aidarbn/platform-go/kit/pgdb"
	"github.com/aidarbn/platform-go/kit/platform"
	"github.com/aidarbn/platform-go/kit/webhookx"
)

// database creates an empty database for one test, so processed ids and jobs of an
// earlier run never meet this one.
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
	name := fmt.Sprintf("webhook_test_%d", time.Now().UnixNano())
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

func open(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgdb.Open(context.Background(), pgdb.Config{URL: dsn})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// The receiver against a real database: a webhook processed once however often it
// comes, a failed handler rolled back and processed on the retry, bad requests refused.
func TestReceiver(t *testing.T) {
	pool := open(t, database(t))
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "CREATE TABLE payments (webhook_id text PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}

	secret := mustSecret(t, webhookx.NewSecret())
	var calls atomic.Int32
	var failNext atomic.Bool
	recv, err := webhookx.NewReceiver(webhookx.ReceiverConfig{
		Source:  "kaspi",
		Secrets: []webhookx.Secret{secret},
		DB:      pool,
		MaxBody: 1024,
		Handle: func(ctx context.Context, tx pgx.Tx, e webhookx.Event) error {
			calls.Add(1)
			if _, err := tx.Exec(ctx, "INSERT INTO payments VALUES ($1)", e.ID); err != nil {
				return err
			}
			if failNext.Swap(false) {
				return errors.New("the domain refused")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(recv)
	defer srv.Close()

	send := func(id string, at time.Time, body []byte, method string) int {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL, bytes.NewReader(body))
		webhookx.SetHeaders(req.Header, secret, id, at, body)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	body := []byte(`{"type":"payment.succeeded"}`)
	payments := func() int {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM payments").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	for range 2 {
		if code := send("msg_a", time.Now(), body, http.MethodPost); code != http.StatusNoContent {
			t.Fatalf("status = %d", code)
		}
	}
	if calls.Load() != 1 || payments() != 1 {
		t.Fatalf("a repeated webhook: %d calls, %d payments", calls.Load(), payments())
	}

	failNext.Store(true)
	if code := send("msg_b", time.Now(), body, http.MethodPost); code != http.StatusInternalServerError {
		t.Fatalf("failed handler: status = %d", code)
	}
	if payments() != 1 {
		t.Fatal("the change of a failed handler was kept")
	}
	if code := send("msg_b", time.Now(), body, http.MethodPost); code != http.StatusNoContent || payments() != 2 {
		t.Fatalf("retry after a failure: status %d, %d payments", code, payments())
	}

	// Copies arriving together are processed once.
	var wg sync.WaitGroup
	before := calls.Load()
	for range 8 {
		wg.Go(func() { send("msg_c", time.Now(), body, http.MethodPost) })
	}
	wg.Wait()
	if calls.Load()-before != 1 {
		t.Fatalf("8 concurrent copies: %d calls", calls.Load()-before)
	}

	for name, c := range map[string]struct {
		code int
		do   func() int
	}{
		"stale":     {http.StatusUnauthorized, func() int { return send("msg_d", time.Now().Add(-time.Hour), body, http.MethodPost) }},
		"get":       {http.StatusMethodNotAllowed, func() int { return send("msg_e", time.Now(), body, http.MethodGet) }},
		"too large": {http.StatusRequestEntityTooLarge, func() int { return send("msg_f", time.Now(), bytes.Repeat([]byte("x"), 2048), http.MethodPost) }},
		"forged": {http.StatusUnauthorized, func() int {
			req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
			webhookx.SetHeaders(req.Header, mustSecret(t, webhookx.NewSecret()), "msg_g", time.Now(), body)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			return resp.StatusCode
		}},
		"no headers": {http.StatusBadRequest, func() int {
			resp, err := http.Post(srv.URL, "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			return resp.StatusCode
		}},
	} {
		if code := c.do(); code != c.code {
			t.Errorf("%s: status = %d, want %d", name, code, c.code)
		}
	}
}

// End to end: a webhook queued in a rolled back transaction never leaves, one queued in
// a committed transaction reaches a receiver through River, signed and processed once.
func TestSendThroughRiver(t *testing.T) {
	dsn := database(t)
	secret := mustSecret(t, webhookx.NewSecret())

	received := make(chan webhookx.Event, 4)
	recv, err := webhookx.NewReceiver(webhookx.ReceiverConfig{
		Source: "shop", Secrets: []webhookx.Secret{secret}, DB: open(t, dsn),
		Handle: func(_ context.Context, _ pgx.Tx, e webhookx.Event) error { received <- e; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(recv)
	defer srv.Close()

	sender, err := webhookx.NewSender(webhookx.SenderConfig{
		Endpoints: map[string]webhookx.Endpoint{"crm": {URL: srv.URL, Secret: secret}},
	})
	if err != nil {
		t.Fatal(err)
	}

	apps := make(chan *platform.App, 1)
	started, errCh := make(chan string, 1), make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cfg := platform.Config{
		Service: "webhook-test", OpsAddr: "127.0.0.1:0", ShutdownTimeout: 5 * time.Second,
		Logger:    logx.New(logx.Options{Writer: io.Discard}),
		OnStarted: func(addr string) { started <- addr },
	}
	modules := []platform.Module{
		postgres.New(postgres.Config{URL: dsn}),
		riverx.New(riverx.Config{Work: true, JobTimeout: 10 * time.Second}),
	}
	go func() {
		errCh <- platform.RunContext(ctx, cfg, modules, func(app *platform.App) error {
			riverx.AddWorker(app, sender)
			apps <- app
			return nil
		})
	}()
	select {
	case <-started:
	case err := <-errCh:
		t.Fatalf("Run returned before startup: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("the service did not start")
	}
	t.Cleanup(func() {
		cancel()
		if err := <-errCh; err != nil {
			t.Errorf("Run: %v", err)
		}
	})
	app := <-apps
	queue, pool := riverx.QueueFrom(app), postgres.Pool(app)

	var committedID string
	for _, commit := range []bool{false, true} {
		err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
			id, err := sender.Enqueue(context.Background(), queue, tx, "crm", "order.paid", map[string]bool{"commit": commit})
			if err != nil {
				return err
			}
			if !commit {
				return errors.New("roll back")
			}
			committedID = id
			return nil
		})
		if commit && err != nil {
			t.Fatalf("commit: %v", err)
		}
	}

	select {
	case e := <-received:
		var msg struct {
			Type string          `json:"type"`
			Data map[string]bool `json:"data"`
		}
		if err := json.Unmarshal(e.Body, &msg); err != nil || e.ID != committedID || msg.Type != "order.paid" || !msg.Data["commit"] {
			t.Fatalf("received %s %s, %v", e.ID, e.Body, err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the committed webhook did not arrive")
	}
	select {
	case e := <-received:
		t.Fatalf("an unexpected webhook arrived: %s", e.Body)
	case <-time.After(time.Second):
	}

	// The attempts the job carries reached River through riverx.Queue.
	rows, err := pool.Query(context.Background(), "SELECT max_attempts FROM river_job WHERE kind = 'webhook_delivery'")
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil || len(attempts) != 1 || attempts[0] != webhookx.DeliveryAttempts {
		t.Fatalf("max_attempts of the jobs = %v, %v; want one job with %d", attempts, err, webhookx.DeliveryAttempts)
	}
}
