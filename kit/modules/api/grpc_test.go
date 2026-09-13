package api_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"

	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/pgdb"
	"github.com/aidarbn/platform-go/kit/platform"
)

func TestRateLimit(t *testing.T) {
	extraWire = func(app *platform.App) {
		api.PublicMethods(app, func(method string) bool { return strings.HasSuffix(method, "/OldEcho") })
	}
	t.Cleanup(func() { extraWire = nil })
	s := start(t, api.Config{RateLimit: api.RateLimit{RPS: 0.001, Burst: 2, PublicRPS: 0.001, PublicBurst: 5}})

	from := func(ip string) map[string]string { return map[string]string{"X-Forwarded-For": ip} }
	var codes []int
	for range 3 {
		code, _, _ := s.do("POST", "/v1/echo", `{"message_text":"x"}`, from("10.0.0.1"))
		codes = append(codes, code)
	}
	if fmt.Sprint(codes) != "[200 200 429]" {
		t.Errorf("codes = %v", codes)
	}
	// Another client has its own bucket.
	if code, _, _ := s.do("POST", "/v1/echo", `{"message_text":"x"}`, from("10.0.0.2")); code != http.StatusOK {
		t.Errorf("another client: %d", code)
	}
	// Public methods have their own, larger bucket.
	for i := range 5 {
		if code, _, _ := s.do("POST", "/v1/old-echo", `{"message_text":"x"}`, from("10.0.0.1")); code != http.StatusOK {
			t.Errorf("public call %d: %d", i, code)
		}
	}
	if !strings.Contains(scrape(t, s.ops), `api_rate_limited_total{method="/platformtest.v1.EchoService/Echo"} 1`) {
		t.Error("refused calls are not counted")
	}
}

// Methods marked deprecated in the proto file are counted and logged, without a list in code.
func TestDeprecatedMethods(t *testing.T) {
	s := start(t, api.Config{})
	s.do("POST", "/v1/old-echo", `{"message_text":"x"}`, map[string]string{"User-Agent": "old-app/1.0"})
	s.do("POST", "/v1/echo", `{"message_text":"x"}`, nil)

	metrics := scrape(t, s.ops)
	if !strings.Contains(metrics, `api_deprecated_calls_total{method="/platformtest.v1.EchoService/OldEcho"} 1`) {
		t.Error("the deprecated call is not counted")
	}
	if strings.Contains(metrics, `api_deprecated_calls_total{method="/platformtest.v1.EchoService/Echo"}`) {
		t.Error("a current method is counted as deprecated")
	}
	if logs := s.logs.String(); !strings.Contains(logs, "deprecated method called") || !strings.Contains(logs, "old-app/1.0") {
		t.Errorf("no warning with the caller:\n%s", logs)
	}
}

func TestIdempotency(t *testing.T) {
	extraWire = func(app *platform.App) {
		api.RequireIdempotency(app, "/platformtest.v1.EchoService/CreateThing")
	}
	t.Cleanup(func() { extraWire = nil })
	s := start(t, api.Config{}, api.WithIdempotencyStore(api.NewMemoryIdempotencyStore()))
	thingCalls.Store(0)

	key := func(k string, extra ...string) map[string]string {
		h := map[string]string{"Idempotency-Key": k}
		for i := 0; i+1 < len(extra); i += 2 {
			h[extra[i]] = extra[i+1]
		}
		return h
	}

	// A method that requires a key refuses a call without one.
	if code, body, _ := s.do("POST", "/v1/things", `{"name":"a"}`, nil); code != http.StatusBadRequest || !strings.Contains(body, "Idempotency-Key is required") {
		t.Fatalf("without a key: %d %s", code, body)
	}

	// A repeat gets the first answer, and the handler runs once.
	_, first, _ := s.do("POST", "/v1/things", `{"name":"a"}`, key("k1", "X-Request-ID", "first"))
	code, second, _ := s.do("POST", "/v1/things", `{"name":"a"}`, key("k1", "X-Request-ID", "second"))
	if code != http.StatusOK || first != second || thingCalls.Load() != 1 {
		t.Errorf("repeat: %d, calls %d\n%s\n%s", code, thingCalls.Load(), first, second)
	}

	// The same key with another payload is a client bug.
	if code, body, _ := s.do("POST", "/v1/things", `{"name":"b"}`, key("k1")); code != http.StatusConflict || !strings.Contains(body, "different payload") {
		t.Errorf("another payload: %d %s", code, body)
	}

	// A final failure is remembered; a failure that says try again is not.
	calls := func(kind, k string) int {
		code, _, _ := s.do("GET", "/v1/fail/"+kind, "", key(k))
		return code
	}
	if a, b := calls("not_found", "k2"), calls("not_found", "k2"); a != http.StatusNotFound || b != http.StatusNotFound {
		t.Errorf("not found: %d %d", a, b)
	}
	if a, b := calls("unavailable", "k3"), calls("unavailable", "k3"); a != http.StatusServiceUnavailable || b != http.StatusServiceUnavailable {
		t.Errorf("unavailable: %d %d", a, b)
	}

	// A repeat while the first call is still running is told so.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.do("GET", "/v1/fail/slow", "", key("k4"))
	}()
	time.Sleep(300 * time.Millisecond)
	if code, body, _ := s.do("GET", "/v1/fail/slow", "", key("k4")); code != http.StatusConflict || !strings.Contains(body, "already in progress") {
		t.Errorf("concurrent repeat: %d %s", code, body)
	}
	wg.Wait()
}

// The postgres store: one record per user, method and key, and a retry taken by exactly
// one of two racing repeats.
func TestPostgresIdempotencyStore(t *testing.T) {
	url := os.Getenv("DATABASE_TEST_URL")
	if url == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgdb.Open(ctx, pgdb.Config{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	store, err := api.NewPostgresIdempotencyStore(ctx, pool)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	key := fmt.Sprintf("test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM platform_idempotency_keys WHERE key = $1", key)
	})

	rec := api.IdempotencyRecord{User: "u1", Method: "/x.v1.S/Create", Key: key, Fingerprint: "f1", Status: "in_progress", ExpiresAt: time.Now().Add(time.Minute)}
	first, created, err := store.Begin(ctx, rec)
	if err != nil || !created {
		t.Fatalf("Begin = %v, %v", created, err)
	}
	again, created, err := store.Begin(ctx, rec)
	if err != nil || created || again.ID != first.ID || again.Status != "in_progress" {
		t.Fatalf("second Begin = %+v, %v, %v", again, created, err)
	}
	if other, created, err := store.Begin(ctx, api.IdempotencyRecord{User: "u2", Method: rec.Method, Key: key, Fingerprint: "f1", Status: "in_progress", ExpiresAt: rec.ExpiresAt}); err != nil || !created || other.ID == first.ID {
		t.Errorf("another user shares the key: %v %v", created, err)
	}

	first.Status, first.Code, first.Message, first.ExpiresAt = "retry", 14, "down", time.Now().Add(time.Minute)
	if err := store.Finish(ctx, first); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	took := 0
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.Retake(ctx, first.ID, time.Now().Add(time.Minute))
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				took++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if took != 1 {
		t.Errorf("%d repeats took the retry", took)
	}

	first.ExpiresAt = time.Now().Add(-time.Second)
	first.Status = "completed"
	if err := store.Finish(ctx, first); err != nil {
		t.Fatal(err)
	}
	if n, err := store.DeleteExpired(ctx, time.Now()); err != nil || n < 1 {
		t.Errorf("DeleteExpired = %d, %v", n, err)
	}
}

// A REST call with a traceparent reaches the gRPC handler in the same trace: the gateway
// carries it over its connection.
func TestTraceContinuesThroughGateway(t *testing.T) {
	seen := make(chan string, 1)
	extraWire = func(app *platform.App) {
		api.AddUnaryInterceptor(app, func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			seen <- trace.SpanContextFromContext(ctx).TraceID().String()
			return h(ctx, req)
		})
	}
	t.Cleanup(func() { extraWire = nil })
	s := start(t, api.Config{})

	s.do("POST", "/v1/echo", `{"message_text":"x"}`, map[string]string{
		"traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	})
	select {
	case got := <-seen:
		if got != "4bf92f3577b34da6a3ce929d0e0e4736" {
			t.Errorf("trace id in the handler = %s", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the handler was not called")
	}
}
