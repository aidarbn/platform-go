package webhookx_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/aidarbn/platform-go/kit/logx"
	"github.com/aidarbn/platform-go/kit/webhookx"
)

func mustSecret(t *testing.T, s string) webhookx.Secret {
	t.Helper()
	secret, err := webhookx.ParseSecret(s)
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

// The vector of the reference library, github.com/standard-webhooks/standard-webhooks.
func TestSignMatchesStandardWebhooks(t *testing.T) {
	secret := mustSecret(t, "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw")
	got := webhookx.Sign(secret, "msg_p5jXN8AQM9LWM0D4loKWxJek", time.Unix(1614265330, 0), []byte(`{"test": 2432232314}`))
	if want := "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="; got != want {
		t.Fatalf("Sign = %s, want %s", got, want)
	}
}

func TestParseSecret(t *testing.T) {
	s := webhookx.NewSecret()
	if !strings.HasPrefix(s, "whsec_") {
		t.Fatalf("NewSecret = %s", s)
	}
	secret := mustSecret(t, s)
	if secret.String() != s {
		t.Fatalf("String = %s, want %s", secret, s)
	}
	for _, bad := range []string{"", "whsec_", "whsec_not base64!", "whsec_c2hvcnQ="} {
		if _, err := webhookx.ParseSecret(bad); err == nil {
			t.Errorf("ParseSecret(%q): no error", bad)
		}
	}
}

func TestVerify(t *testing.T) {
	current, previous := mustSecret(t, webhookx.NewSecret()), mustSecret(t, webhookx.NewSecret())
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"type":"order.paid"}`)

	signed := func(secret webhookx.Secret, at time.Time) http.Header {
		h := http.Header{}
		webhookx.SetHeaders(h, secret, "msg_1", at, body)
		return h
	}
	secrets := []webhookx.Secret{current, previous}

	if id, at, err := webhookx.Verify(signed(current, now), body, secrets, now, webhookx.DefaultTolerance); err != nil || id != "msg_1" || !at.Equal(now) {
		t.Fatalf("Verify = %s, %s, %v", id, at, err)
	}
	// A sender still on the previous secret during a rotation.
	if _, _, err := webhookx.Verify(signed(previous, now), body, secrets, now, webhookx.DefaultTolerance); err != nil {
		t.Fatalf("previous secret: %v", err)
	}
	// Several signatures in one header, an unknown version among them.
	h := signed(current, now)
	h.Set(webhookx.HeaderSignature, "v2,AAAA v1,bm90IGl0 "+h.Get(webhookx.HeaderSignature))
	if _, _, err := webhookx.Verify(h, body, secrets, now, webhookx.DefaultTolerance); err != nil {
		t.Fatalf("several signatures: %v", err)
	}

	cases := map[string]struct {
		h    http.Header
		body []byte
		want error
	}{
		"tampered body": {signed(current, now), []byte(`{"type":"order.refunded"}`), webhookx.ErrBadSignature},
		"other secret":  {signed(mustSecret(t, webhookx.NewSecret()), now), body, webhookx.ErrBadSignature},
		"too old":       {signed(current, now.Add(-6*time.Minute)), body, webhookx.ErrStale},
		"too new":       {signed(current, now.Add(6*time.Minute)), body, webhookx.ErrStale},
		"no headers":    {http.Header{}, body, webhookx.ErrMissingHeaders},
	}
	badTS := signed(current, now)
	badTS.Set(webhookx.HeaderTimestamp, "yesterday")
	cases["bad timestamp"] = struct {
		h    http.Header
		body []byte
		want error
	}{badTS, body, webhookx.ErrBadTimestamp}

	for name, c := range cases {
		if _, _, err := webhookx.Verify(c.h, c.body, secrets, now, webhookx.DefaultTolerance); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
}

func TestNewSenderChecksEndpoints(t *testing.T) {
	secret := mustSecret(t, webhookx.NewSecret())
	for name, e := range map[string]webhookx.Endpoint{
		"no scheme": {URL: "example.com/hook", Secret: secret},
		"ftp":       {URL: "ftp://example.com/hook", Secret: secret},
		"no secret": {URL: "https://example.com/hook"},
	} {
		if _, err := webhookx.NewSender(webhookx.SenderConfig{Endpoints: map[string]webhookx.Endpoint{"crm": e}}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// fakeQueue records the job Enqueue inserts.
type fakeQueue struct{ args river.JobArgs }

func (q *fakeQueue) InsertTx(_ context.Context, _ pgx.Tx, args river.JobArgs, _ *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	q.args = args
	return &rivertype.JobInsertResult{}, nil
}

func job(args webhookx.DeliveryArgs, attempt int) *river.Job[webhookx.DeliveryArgs] {
	return &river.Job[webhookx.DeliveryArgs]{
		JobRow: &rivertype.JobRow{Attempt: attempt, MaxAttempts: webhookx.DeliveryAttempts},
		Args:   args,
	}
}

// Enqueue builds a Standard Webhooks body and Work posts it signed, so the receiving
// side verifies it; non-2xx answers are retried, 410 cancels, the last attempt alerts.
func TestSenderDelivers(t *testing.T) {
	secret := mustSecret(t, webhookx.NewSecret())
	var status atomic.Int32
	status.Store(http.StatusNoContent)
	got := make(chan *http.Request, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		got <- r
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte("busy"))
	}))
	defer srv.Close()

	var logs bytes.Buffer
	now := time.Now()
	sender, err := webhookx.NewSender(webhookx.SenderConfig{
		Endpoints: map[string]webhookx.Endpoint{"crm": {URL: srv.URL, Secret: secret}},
		Logger:    logx.New(logx.Options{Writer: &logs}),
		Now:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	q := &fakeQueue{}
	ctx := context.Background()
	if _, err := sender.Enqueue(ctx, q, nil, "erp", "order.paid", nil); err == nil {
		t.Fatal("unknown endpoint: no error")
	}
	id, err := sender.Enqueue(ctx, q, nil, "crm", "order.paid", map[string]int{"order": 7})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	args := q.args.(webhookx.DeliveryArgs)
	if args.ID != id || !strings.HasPrefix(id, "msg_") || args.Endpoint != "crm" {
		t.Fatalf("args = %+v, id %s", args, id)
	}
	var msg struct {
		Type string         `json:"type"`
		Data map[string]int `json:"data"`
	}
	if err := json.Unmarshal(args.Body, &msg); err != nil || msg.Type != "order.paid" || msg.Data["order"] != 7 {
		t.Fatalf("body = %s, %v", args.Body, err)
	}

	if err := sender.Work(ctx, job(args, 1)); err != nil {
		t.Fatalf("Work: %v", err)
	}
	r := <-got
	body, _ := io.ReadAll(r.Body)
	if _, _, err := webhookx.Verify(r.Header, body, []webhookx.Secret{secret}, now, webhookx.DefaultTolerance); err != nil {
		t.Fatalf("the receiving side cannot verify: %v", err)
	}
	if r.Header.Get(webhookx.HeaderID) != id || r.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %v", r.Header)
	}

	status.Store(http.StatusServiceUnavailable)
	err = sender.Work(ctx, job(args, 1))
	var cancel *river.JobCancelError
	if err == nil || errors.As(err, &cancel) || !strings.Contains(err.Error(), "503: busy") {
		t.Fatalf("503: err = %v, want a retry", err)
	}
	<-got
	if strings.Contains(logs.String(), webhookx.AlertDeliveryFailed) {
		t.Fatal("alert before the last attempt")
	}
	if err := sender.Work(ctx, job(args, webhookx.DeliveryAttempts)); err == nil {
		t.Fatal("last attempt: no error")
	}
	<-got
	if !strings.Contains(logs.String(), `"alert":"`+webhookx.AlertDeliveryFailed+`"`) {
		t.Fatalf("no alert after the last attempt:\n%s", logs.String())
	}

	status.Store(http.StatusGone)
	if err := sender.Work(ctx, job(args, 1)); !errors.As(err, &cancel) {
		t.Fatalf("410: err = %v, want a cancel", err)
	}
	<-got

	args.Endpoint = "removed"
	if err := sender.Work(ctx, job(args, 1)); !errors.As(err, &cancel) {
		t.Fatalf("removed endpoint: err = %v, want a cancel", err)
	}
}
