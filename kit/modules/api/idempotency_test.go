package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/aidarbn/platform-go/kit/confx"
	testv1 "github.com/aidarbn/platform-go/kit/internal/testapi/platformtest/v1"
	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/pgdb"
	"github.com/aidarbn/platform-go/kit/platform"
)

// withUser names the caller of a call from its X-User header, the way a project's
// authentication would.
func withUser(app *platform.App) {
	api.IdempotencyUser(app, func(ctx context.Context) string {
		md, _ := metadata.FromIncomingContext(ctx)
		return strings.Join(md.Get("x-user"), "")
	})
}

const failMethod = "/platformtest.v1.EchoService/Fail"

// A success and the client errors are remembered; a failure that may pass — Internal,
// Unavailable, Canceled, a panic — lets the next repeat run, and Internal never leaks
// what the handler said.
func TestIdempotencyRemembersOnlyFinalOutcomes(t *testing.T) {
	extraWire = func(app *platform.App) {
		api.IdempotencyMethods(app, failMethod)
		withUser(app)
	}
	t.Cleanup(func() { extraWire = nil })
	s := start(t, api.Config{}, api.WithIdempotencyStore(api.NewMemoryIdempotencyStore()))

	for _, tc := range []struct {
		kind string
		code int
		runs int32
	}{
		{"none", http.StatusOK, 1},
		{"not_found", http.StatusNotFound, 1},
		{"details", http.StatusBadRequest, 1},
		{"internal", http.StatusInternalServerError, 2},
		{"unavailable", http.StatusServiceUnavailable, 2},
		{"external", http.StatusBadGateway, 2},
		{"external_timeout", http.StatusGatewayTimeout, 2},
		{"panic", http.StatusInternalServerError, 2},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			failCalls.Store(0)
			h := map[string]string{"Idempotency-Key": "final-" + tc.kind, "X-User": "alice"}
			code1, body1, _ := s.do("GET", "/v1/fail/"+tc.kind, "", h)
			code2, body2, _ := s.do("GET", "/v1/fail/"+tc.kind, "", h)
			if code1 != tc.code || code2 != tc.code {
				t.Errorf("codes %d %d, want %d", code1, code2, tc.code)
			}
			if got := failCalls.Load(); got != tc.runs {
				t.Errorf("the handler ran %d times, want %d", got, tc.runs)
			}
			if body1 != body2 {
				t.Errorf("the repeat differs:\n%s\n%s", body1, body2)
			}
			if strings.Contains(body2, "password") {
				t.Errorf("internal details leaked: %s", body2)
			}
		})
	}
	// The details of a remembered failure come back with the repeat.
	h := map[string]string{"Idempotency-Key": "final-details", "X-User": "alice"}
	if _, body, _ := s.do("GET", "/v1/fail/details", "", h); !strings.Contains(body, `"field":"items"`) || !strings.Contains(body, "BadRequest") {
		t.Errorf("details are not replayed: %s", body)
	}

	metrics := scrape(t, s.ops)
	for _, want := range []string{
		`api_idempotency_total{method="/platformtest.v1.EchoService/Fail",result="replay"}`,
		`api_idempotency_total{method="/platformtest.v1.EchoService/Fail",result="stored"} 3`,
		`api_idempotency_total{method="/platformtest.v1.EchoService/Fail",result="retry"} 10`,
	} {
		if !strings.Contains(metrics, want) {
			t.Errorf("no %s in\n%s", want, grepLines(metrics, "api_idempotency_total"))
		}
	}
}

func grepLines(text, part string) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, part) {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// A key counts only for a known caller and, by default, only on a method that changes
// something: anonymous callers would share their keys, and reads would write to the
// database.
func TestIdempotencyKeysCountForKnownCallersOnMutatingMethods(t *testing.T) {
	extraWire = func(app *platform.App) {
		api.RequireIdempotency(app, "/platformtest.v1.EchoService/CreateThing")
		withUser(app)
	}
	t.Cleanup(func() { extraWire = nil })
	s := start(t, api.Config{}, api.WithIdempotencyStore(api.NewMemoryIdempotencyStore()))

	thingCalls.Store(0)
	anon := map[string]string{"Idempotency-Key": "shared"}
	_, first, _ := s.do("POST", "/v1/things", `{"name":"a"}`, anon)
	_, second, _ := s.do("POST", "/v1/things", `{"name":"a"}`, anon)
	if thingCalls.Load() != 2 || first == second {
		t.Errorf("an anonymous key was used: calls %d\n%s\n%s", thingCalls.Load(), first, second)
	}
	// A required key is still required of an anonymous caller.
	if code, _, _ := s.do("POST", "/v1/things", `{"name":"a"}`, nil); code != http.StatusBadRequest {
		t.Errorf("anonymous without a key: %d", code)
	}

	// A POST counts, a GET does not.
	thingCalls.Store(0)
	alice := map[string]string{"Idempotency-Key": "k", "X-User": "alice"}
	s.do("POST", "/v1/things", `{"name":"a"}`, alice)
	s.do("POST", "/v1/things", `{"name":"a"}`, alice)
	if thingCalls.Load() != 1 {
		t.Errorf("the POST ran %d times", thingCalls.Load())
	}
	failCalls.Store(0)
	s.do("GET", "/v1/fail/not_found", "", alice)
	s.do("GET", "/v1/fail/not_found", "", alice)
	if failCalls.Load() != 2 {
		t.Errorf("a GET was remembered: ran %d times", failCalls.Load())
	}
}

// A list of methods wins over the default: a POST outside it is not remembered.
func TestIdempotencyMethodsWinOverTheDefault(t *testing.T) {
	extraWire = func(app *platform.App) {
		api.IdempotencyMethods(app, failMethod, "/platformtest.v1.EchoService/Gone")
		withUser(app)
	}
	t.Cleanup(func() { extraWire = nil })
	s := start(t, api.Config{}, api.WithIdempotencyStore(api.NewMemoryIdempotencyStore()))
	h := map[string]string{"Idempotency-Key": "k", "X-User": "alice"}

	thingCalls.Store(0)
	s.do("POST", "/v1/things", `{"name":"a"}`, h)
	s.do("POST", "/v1/things", `{"name":"a"}`, h)
	if thingCalls.Load() != 2 {
		t.Errorf("a POST outside the list was remembered: ran %d times", thingCalls.Load())
	}
	failCalls.Store(0)
	s.do("GET", "/v1/fail/not_found", "", h)
	s.do("GET", "/v1/fail/not_found", "", h)
	if failCalls.Load() != 1 {
		t.Errorf("a listed GET ran %d times", failCalls.Load())
	}
	if !strings.Contains(s.logs.String(), "api.IdempotencyMethods names a method the API does not serve") {
		t.Error("no warning about a method the API does not serve")
	}
}

// Without IdempotencyUser keys are ignored, and the start says so.
func TestIdempotencyWithoutUser(t *testing.T) {
	s := start(t, api.Config{}, api.WithIdempotencyStore(api.NewMemoryIdempotencyStore()))
	thingCalls.Store(0)
	h := map[string]string{"Idempotency-Key": "k"}
	s.do("POST", "/v1/things", `{"name":"a"}`, h)
	s.do("POST", "/v1/things", `{"name":"a"}`, h)
	if thingCalls.Load() != 2 {
		t.Errorf("ran %d times", thingCalls.Load())
	}
	if !strings.Contains(s.logs.String(), "no api.IdempotencyUser names the caller") {
		t.Error("the start does not say keys are ignored")
	}
}

// A client that hangs up does not lose the outcome: it is written on a context of its
// own, and the repeat gets it without running the call again.
func TestIdempotencySaveSurvivesClientCancel(t *testing.T) {
	extraWire = func(app *platform.App) {
		api.IdempotencyMethods(app, failMethod)
		withUser(app)
	}
	t.Cleanup(func() { extraWire = nil })
	s := start(t, api.Config{}, api.WithIdempotencyStore(api.NewMemoryIdempotencyStore()))

	conn, err := grpc.NewClient(s.module.GRPCAddr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := testv1.NewEchoServiceClient(conn)
	md := metadata.Pairs("idempotency-key", "cancel-1", "x-user", "alice")

	failCalls.Store(0)
	ctx, cancel := context.WithTimeout(metadata.NewOutgoingContext(context.Background(), md), 50*time.Millisecond)
	_, err = client.Fail(ctx, &testv1.FailRequest{Kind: "slow_ok"})
	cancel()
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("first call: %v", err)
	}
	time.Sleep(500 * time.Millisecond) // the handler finishes and the outcome is saved
	if _, err := client.Fail(metadata.NewOutgoingContext(context.Background(), md), &testv1.FailRequest{Kind: "slow_ok"}); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if failCalls.Load() != 1 {
		t.Errorf("the handler ran %d times", failCalls.Load())
	}
}

// A handler that runs longer than the lease keeps its key: the lease is extended, and
// a repeat is told the call is still running instead of taking the key over.
func TestIdempotencyLeaseIsExtended(t *testing.T) {
	extraWire = func(app *platform.App) {
		api.IdempotencyMethods(app, failMethod)
		withUser(app)
	}
	t.Cleanup(func() { extraWire = nil })
	s := start(t, api.Config{IdempotencyLease: 60 * time.Millisecond}, api.WithIdempotencyStore(api.NewMemoryIdempotencyStore()))
	h := map[string]string{"Idempotency-Key": "long", "X-User": "alice"}

	failCalls.Store(0)
	done := make(chan int, 1)
	go func() {
		code, _, _ := s.do("GET", "/v1/fail/slow_ok", "", h)
		done <- code
	}()
	time.Sleep(200 * time.Millisecond) // past three leases
	if code, body, _ := s.do("GET", "/v1/fail/slow_ok", "", h); code != http.StatusConflict || !strings.Contains(body, "already in progress") {
		t.Errorf("repeat while running: %d %s", code, body)
	}
	if code := <-done; code != http.StatusOK {
		t.Errorf("first: %d", code)
	}
	if code, _, _ := s.do("GET", "/v1/fail/slow_ok", "", h); code != http.StatusOK || failCalls.Load() != 1 {
		t.Errorf("after: %d, ran %d times", code, failCalls.Load())
	}
}

func TestIdempotencyLeaseFromTheEnvironment(t *testing.T) {
	t.Setenv("API_IDEMPOTENCY_LOCK", "45s")
	cfg := api.Load(confx.New(""))
	if cfg.IdempotencyLease != 0 || cfg.IdempotencyLock != 45*time.Second {
		t.Errorf("old name: lease %s, lock %s", cfg.IdempotencyLease, cfg.IdempotencyLock)
	}
	s := start(t, cfg, api.WithIdempotencyStore(api.NewMemoryIdempotencyStore()))
	if !strings.Contains(s.logs.String(), "API_IDEMPOTENCY_LOCK (Config.IdempotencyLock) is deprecated") {
		t.Error("no warning about the old name")
	}

	t.Setenv("API_IDEMPOTENCY_LEASE", "3m")
	if cfg := api.Load(confx.New("")); cfg.IdempotencyLease != 3*time.Minute {
		t.Errorf("lease = %s", cfg.IdempotencyLease)
	}
	os.Unsetenv("API_IDEMPOTENCY_LEASE")
	os.Unsetenv("API_IDEMPOTENCY_LOCK")
	if cfg := api.Load(confx.New("")); cfg.IdempotencyLease != 2*time.Minute {
		t.Errorf("default lease = %s", cfg.IdempotencyLease)
	}
}

// storeContract is what every store must do: one record per user, method and key, a
// retry or an expired lease taken over by exactly one of racing repeats, and fencing — a
// call whose key was taken over neither extends nor overwrites it.
func storeContract(t *testing.T, store api.IdempotencyStore, key string) {
	ctx := context.Background()
	ext, ok := store.(api.IdempotencyLeaseExtender)
	if !ok {
		t.Fatal("the store does not extend leases")
	}
	us := func(d time.Duration) time.Time { return time.Now().Add(d).Truncate(time.Microsecond) }

	rec := api.IdempotencyRecord{User: "u1", Method: "/x.v1.S/Create", Key: key, Fingerprint: "f1", Status: "in_progress", ExpiresAt: us(time.Minute)}
	first, created, err := store.Begin(ctx, rec)
	if err != nil || !created || !first.ExpiresAt.Equal(rec.ExpiresAt) {
		t.Fatalf("Begin = %+v, %v, %v", first, created, err)
	}
	again, created, err := store.Begin(ctx, rec)
	if err != nil || created || again.ID != first.ID || again.Status != "in_progress" {
		t.Fatalf("second Begin = %+v, %v, %v", again, created, err)
	}
	if other, created, err := store.Begin(ctx, api.IdempotencyRecord{User: "u2", Method: rec.Method, Key: key, Fingerprint: "f1", Status: "in_progress", ExpiresAt: rec.ExpiresAt}); err != nil || !created || other.ID == first.ID {
		t.Errorf("another user shares the key: %v %v", created, err)
	}

	// A live lease is not taken over, and is extended by its holder only.
	if ok, err := store.Retake(ctx, first.ID, us(time.Minute)); ok || err != nil {
		t.Errorf("a live lease was taken over: %v %v", ok, err)
	}
	if ok, err := ext.ExtendLease(ctx, first.ID, us(-time.Hour), us(time.Hour)); ok || err != nil {
		t.Errorf("a stranger extended the lease: %v %v", ok, err)
	}
	short := us(-time.Second) // the holder extends it into the past: as if it expired
	if ok, err := ext.ExtendLease(ctx, first.ID, first.ExpiresAt, short); !ok || err != nil {
		t.Fatalf("the holder could not extend: %v %v", ok, err)
	}

	// The expired lease is taken over by exactly one of racing repeats.
	newLease := us(time.Minute)
	var wg sync.WaitGroup
	var took atomic.Int32
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.Retake(ctx, first.ID, newLease)
			if err != nil {
				t.Error(err)
			}
			if ok {
				took.Add(1)
			}
		}()
	}
	wg.Wait()
	if took.Load() != 1 {
		t.Fatalf("%d repeats took the expired lease", took.Load())
	}

	// The stale owner neither extends nor overwrites the new one.
	if ok, err := ext.ExtendLease(ctx, first.ID, short, us(time.Hour)); ok || err != nil {
		t.Errorf("the stale owner extended: %v %v", ok, err)
	}
	stale := first
	stale.Status, stale.Code, stale.Message, stale.ExpiresAt, stale.Lease = "completed", 0, "", us(time.Hour), short
	if err := store.Finish(ctx, stale); !errors.Is(err, api.ErrIdempotencyLeaseLost) {
		t.Errorf("the stale owner finished: %v", err)
	}

	// The new owner finishes with a retry, which exactly one repeat takes.
	owner := first
	owner.Status, owner.Code, owner.Message, owner.ExpiresAt, owner.Lease = "retry", 14, "down", us(time.Minute), newLease
	if err := store.Finish(ctx, owner); err != nil {
		t.Fatalf("the owner could not finish: %v", err)
	}
	if got, _, _ := store.Begin(ctx, rec); got.Status != "retry" || got.Code != 14 {
		t.Errorf("after the owner: %+v", got)
	}
	took.Store(0)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := store.Retake(ctx, first.ID, us(time.Minute)); ok {
				took.Add(1)
			}
		}()
	}
	wg.Wait()
	if took.Load() != 1 {
		t.Errorf("%d repeats took the retry", took.Load())
	}

	// A finish without a lease, as an older caller writes, is not checked.
	done := first
	done.Status, done.ExpiresAt, done.Lease = "completed", us(-time.Second), time.Time{}
	if err := store.Finish(ctx, done); err != nil {
		t.Fatal(err)
	}

	// A completed record past its expiry, not yet deleted, is taken over by exactly one
	// of racing repeats; a fresh one is not taken over at all.
	took.Store(0)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.Retake(ctx, first.ID, us(time.Minute))
			if err != nil {
				t.Error(err)
			}
			if ok {
				took.Add(1)
			}
		}()
	}
	wg.Wait()
	if took.Load() != 1 {
		t.Errorf("%d repeats took the expired outcome", took.Load())
	}
	if got, _, _ := store.Begin(ctx, rec); got.Status != "in_progress" {
		t.Errorf("after taking the expired outcome: %+v", got)
	}
	fresh := first
	fresh.Status, fresh.ExpiresAt, fresh.Lease = "completed", us(time.Hour), time.Time{}
	if err := store.Finish(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.Retake(ctx, first.ID, us(time.Minute)); ok || err != nil {
		t.Errorf("a fresh outcome was taken over: %v %v", ok, err)
	}
	if err := store.Finish(ctx, done); err != nil {
		t.Fatal(err)
	}
	if n, err := store.DeleteExpired(ctx, time.Now()); err != nil || n < 1 {
		t.Errorf("DeleteExpired = %d, %v", n, err)
	}
	// A finish for a record that is gone, with a lease, is a lost lease.
	gone := done
	gone.Lease = us(time.Minute)
	if err := store.Finish(ctx, gone); !errors.Is(err, api.ErrIdempotencyLeaseLost) {
		t.Errorf("finish of a removed record: %v", err)
	}
}

func TestMemoryIdempotencyStoreContract(t *testing.T) {
	storeContract(t, api.NewMemoryIdempotencyStore(), "memory")
}

func TestPostgresIdempotencyStoreContract(t *testing.T) {
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
		t.Fatal(err)
	}
	key := fmt.Sprintf("contract-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM platform_idempotency_keys WHERE key = $1", key)
	})
	storeContract(t, store, key)
}
