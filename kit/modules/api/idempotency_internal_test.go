package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	testv1 "github.com/aidarbn/platform-go/kit/internal/testapi/platformtest/v1"
	"github.com/aidarbn/platform-go/kit/pgdb"
)

const (
	createThing = "/platformtest.v1.EchoService/CreateThing"
	failCall    = "/platformtest.v1.EchoService/Fail"
)

func TestMutatingMethods(t *testing.T) {
	for method, want := range map[string]bool{
		createThing:                         true,  // POST
		failCall:                            false, // GET
		"/platformtest.v1.EchoService/Echo": true,
		"/unknown.v1.Service/Call":          true, // not in the registry: counts as mutating
		"/grpc.health.v1.Health/Check":      true, // no HTTP binding
		"bad":                               true,
	} {
		if got := mutating(method); got != want {
			t.Errorf("%s: %v, want %v", method, got, want)
		}
	}
}

type marked struct {
	error
	retry bool
}

func (m marked) IsRetryable() bool { return m.retry }

// noLease is a store from before leases: it cannot extend them.
type noLease struct{ IdempotencyStore }

func newTestIdempotency(store IdempotencyStore, log *bytes.Buffer) *idempotency {
	return &idempotency{
		store: store, required: map[string]bool{}, accept: map[string]bool{createThing: true, failCall: true},
		user:      func(ctx context.Context) string { return "alice" },
		retention: time.Hour, lease: time.Minute,
		log: slog.New(slog.NewTextHandler(log, nil)), now: time.Now,
	}
}

func call(i *idempotency, method, key string, h grpc.UnaryHandler) (any, error) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("idempotency-key", key))
	return i.unary(ctx, &testv1.CreateThingRequest{Name: "a"}, &grpc.UnaryServerInfo{FullMethod: method}, h)
}

// RetryableError decides both ways: a client error marked retryable runs again, an
// Internal marked final is remembered.
func TestRetryableErrorOverridesTheCode(t *testing.T) {
	var log bytes.Buffer
	i := newTestIdempotency(NewMemoryIdempotencyStore(), &log)
	runs := 0
	for _, tc := range []struct {
		key  string
		err  error
		runs int
	}{
		{"retry-not-found", marked{status.Error(codes.NotFound, "later"), true}, 2},
		{"final-internal", marked{errors.New("boom"), false}, 1},
		{"canceled", context.Canceled, 2},
	} {
		runs = 0
		h := func(context.Context, any) (any, error) { runs++; return nil, tc.err }
		_, err1 := call(i, createThing, tc.key, h)
		_, err2 := call(i, createThing, tc.key, h)
		if runs != tc.runs || publicStatus(err1).Code() != publicStatus(err2).Code() {
			t.Errorf("%s: ran %d times, want %d; %v / %v", tc.key, runs, tc.runs, err1, err2)
		}
	}
}

// A record written before the status was kept is replayed from its code and message;
// one that cannot be read answers Internal.
func TestOldAndBrokenRecords(t *testing.T) {
	var log bytes.Buffer
	i := newTestIdempotency(NewMemoryIdempotencyStore(), &log)
	if _, err := i.cached(context.Background(), createThing, IdempotencyRecord{Code: int32(codes.NotFound), Message: "no such thing"}); status.Code(err) != codes.NotFound || status.Convert(err).Message() != "no such thing" {
		t.Errorf("old failure: %v", err)
	}
	if _, err := i.cached(context.Background(), createThing, IdempotencyRecord{Code: int32(codes.NotFound), Message: "x", Response: []byte{0xff}}); status.Code(err) != codes.NotFound {
		t.Errorf("unreadable status: %v", err)
	}
	if _, err := i.cached(context.Background(), createThing, IdempotencyRecord{Response: []byte{0xff, 0xff}}); status.Code(err) != codes.Internal {
		t.Errorf("unreadable success: %v", err)
	}
	if _, err := i.cached(context.Background(), "/unknown.v1.S/M", IdempotencyRecord{}); status.Code(err) != codes.Internal {
		t.Errorf("unknown method: %v", err)
	}
	if !bytes.Contains(log.Bytes(), []byte("cannot be read")) {
		t.Error("an unreadable record is not logged")
	}
}

// A success that is not a message cannot be kept, so it is not remembered.
func TestSuccessThatCannotBeKept(t *testing.T) {
	var log bytes.Buffer
	i := newTestIdempotency(NewMemoryIdempotencyStore(), &log)
	runs := 0
	h := func(context.Context, any) (any, error) { runs++; return "not a message", nil }
	call(i, createThing, "plain", h)
	call(i, createThing, "plain", h)
	if runs != 2 {
		t.Errorf("ran %d times", runs)
	}
}

// A store that cannot extend leases still works: the lease is simply not extended.
func TestStoreWithoutLeaseExtension(t *testing.T) {
	var log bytes.Buffer
	i := newTestIdempotency(noLease{NewMemoryIdempotencyStore()}, &log)
	i.lease = 30 * time.Millisecond
	runs := 0
	h := func(context.Context, any) (any, error) {
		runs++
		time.Sleep(50 * time.Millisecond)
		return &testv1.CreateThingResponse{Id: "x"}, nil
	}
	if _, err := call(i, createThing, "k", h); err != nil {
		t.Fatal(err)
	}
	if resp, err := call(i, createThing, "k", h); err != nil || resp.(*testv1.CreateThingResponse).GetId() != "x" || runs != 1 {
		t.Errorf("repeat: %v %v, ran %d", resp, err, runs)
	}
}

// A key too long for the index, and a key on a method outside the list, are handled
// before the store is asked.
func TestKeyChecks(t *testing.T) {
	var log bytes.Buffer
	i := newTestIdempotency(NewMemoryIdempotencyStore(), &log)
	h := func(context.Context, any) (any, error) { return &testv1.CreateThingResponse{}, nil }
	long := string(bytes.Repeat([]byte("k"), maxIdempotencyKey+1))
	if _, err := call(i, createThing, long, h); status.Code(err) != codes.InvalidArgument {
		t.Errorf("long key: %v", err)
	}
	if _, err := call(i, "/platformtest.v1.EchoService/Echo", long, h); err != nil {
		t.Errorf("a key on a method outside the list is not ignored: %v", err)
	}
}

// When the lease is lost while the call runs, the late outcome is not written and the
// loss is logged.
func TestLostLeaseIsNotOverwritten(t *testing.T) {
	var log bytes.Buffer
	store := NewMemoryIdempotencyStore()
	i := newTestIdempotency(store, &log)
	i.lease = 30 * time.Millisecond
	h := func(ctx context.Context, _ any) (any, error) {
		// Another instance takes the key over: the lease the call holds is gone.
		store.mu.Lock()
		for _, r := range store.records {
			r.ExpiresAt = time.Now().Add(time.Hour)
		}
		store.mu.Unlock()
		time.Sleep(40 * time.Millisecond)
		return &testv1.CreateThingResponse{Id: "late"}, nil
	}
	if _, err := call(i, createThing, "lost", h); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	for _, r := range store.records {
		if r.Status != idemInProgress {
			t.Errorf("the late outcome was written: %+v", r)
		}
	}
	store.mu.Unlock()
	if !bytes.Contains(log.Bytes(), []byte("taken over")) {
		t.Errorf("the lost lease is not logged:\n%s", log.String())
	}
}

// flaky is a store whose writes fail in chosen ways.
type flaky struct {
	*MemoryIdempotencyStore
	mu sync.Mutex
	// extendLost: the next extension reaches the store but reports an error, as a
	// timeout after the commit would.
	extendLost bool
	// finishFails: how many Finish calls fail before one goes through.
	finishFails int
}

func (f *flaky) ExtendLease(ctx context.Context, id int64, lease, until time.Time) (bool, error) {
	ok, err := f.MemoryIdempotencyStore.ExtendLease(ctx, id, lease, until)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.extendLost {
		f.extendLost = false
		return false, errors.New("timeout")
	}
	return ok, err
}

func (f *flaky) Finish(ctx context.Context, rec IdempotencyRecord) error {
	f.mu.Lock()
	if f.finishFails > 0 {
		f.finishFails--
		f.mu.Unlock()
		return errors.New("connection reset")
	}
	f.mu.Unlock()
	return f.MemoryIdempotencyStore.Finish(ctx, rec)
}

func statusOf(store *MemoryIdempotencyStore) string {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, r := range store.records {
		return r.Status
	}
	return ""
}

// An extension whose answer was lost still went through: the holder keeps the lease
// and saves its outcome.
func TestExtensionWithALostAnswer(t *testing.T) {
	var log bytes.Buffer
	store := &flaky{MemoryIdempotencyStore: NewMemoryIdempotencyStore(), extendLost: true}
	i := newTestIdempotency(store, &log)
	i.lease = 30 * time.Millisecond
	h := func(context.Context, any) (any, error) {
		time.Sleep(45 * time.Millisecond) // one extension, whose answer is lost
		return &testv1.CreateThingResponse{Id: "x"}, nil
	}
	if _, err := call(i, createThing, "k", h); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(store.MemoryIdempotencyStore); got != idemCompleted {
		t.Errorf("status = %q, want completed\n%s", got, log.String())
	}
}

// A store failure of the final write is retried.
func TestFinishIsRetried(t *testing.T) {
	var log bytes.Buffer
	store := &flaky{MemoryIdempotencyStore: NewMemoryIdempotencyStore(), finishFails: 2}
	i := newTestIdempotency(store, &log)
	if _, err := call(i, createThing, "k", func(context.Context, any) (any, error) { return &testv1.CreateThingResponse{}, nil }); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(store.MemoryIdempotencyStore); got != idemCompleted {
		t.Errorf("status = %q after two failed writes", got)
	}
	store.finishFails = 5
	call(i, createThing, "k2", func(context.Context, any) (any, error) { return &testv1.CreateThingResponse{}, nil })
	if !bytes.Contains(log.Bytes(), []byte("outcome was not saved")) {
		t.Error("a write that keeps failing is not logged")
	}
}

// A panic past the handler's recovery releases the key for a repeat and goes on.
func TestPanicPastTheInnerRecovery(t *testing.T) {
	var log bytes.Buffer
	store := NewMemoryIdempotencyStore()
	i := newTestIdempotency(store, &log)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic was swallowed")
			}
		}()
		call(i, createThing, "k", func(context.Context, any) (any, error) { panic("late") })
	}()
	if got := statusOf(store); got != idemRetry {
		t.Errorf("status = %q, want retry", got)
	}
}

func TestKeyMustBePrintable(t *testing.T) {
	var log bytes.Buffer
	i := newTestIdempotency(NewMemoryIdempotencyStore(), &log)
	h := func(context.Context, any) (any, error) { return &testv1.CreateThingResponse{}, nil }
	for _, key := range []string{"k\xff", "k\x01", "ключ"} {
		if _, err := call(i, createThing, key, h); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%q: %v", key, err)
		}
	}
}

// expire moves every completed record of the store past its retention, as time would,
// before DeleteExpired removes it.
func expire(store *MemoryIdempotencyStore) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, r := range store.records {
		if r.Status == idemCompleted {
			r.ExpiresAt = time.Now().Add(-time.Second)
		}
	}
}

// An outcome past its retention is not replayed even while its record is still there:
// the repeat runs the call again and its outcome is kept.
func TestExpiredOutcomeIsNotReplayed(t *testing.T) {
	var log bytes.Buffer
	store := NewMemoryIdempotencyStore()
	i := newTestIdempotency(store, &log)
	runs := 0
	h := func(context.Context, any) (any, error) {
		runs++
		return &testv1.CreateThingResponse{Id: string(rune('0' + runs))}, nil
	}
	if _, err := call(i, createThing, "k", h); err != nil {
		t.Fatal(err)
	}
	if resp, err := call(i, createThing, "k", h); err != nil || resp.(*testv1.CreateThingResponse).GetId() != "1" || runs != 1 {
		t.Fatalf("a fresh outcome must be replayed: %v %v, runs %d", resp, err, runs)
	}
	expire(store)
	resp, err := call(i, createThing, "k", h)
	if err != nil || resp.(*testv1.CreateThingResponse).GetId() != "2" || runs != 2 {
		t.Fatalf("the expired outcome: %v %v, runs %d", resp, err, runs)
	}
	// The new outcome is kept for the retention again.
	if resp, err := call(i, createThing, "k", h); err != nil || resp.(*testv1.CreateThingResponse).GetId() != "2" || runs != 2 {
		t.Errorf("the new outcome is not replayed: %v %v, runs %d", resp, err, runs)
	}
	if statusOf(store) != idemCompleted {
		t.Errorf("status %q", statusOf(store))
	}
}

// Two repeats of an expired outcome: one runs the call again, the other re-reads the
// record and is refused while it runs — run with -race.
func TestExpiredOutcomeRace(t *testing.T) {
	for name, store := range map[string]func(t *testing.T) IdempotencyStore{
		"memory":   func(*testing.T) IdempotencyStore { return NewMemoryIdempotencyStore() },
		"postgres": pgStore,
	} {
		t.Run(name, func(t *testing.T) {
			var log bytes.Buffer
			s := store(t)
			i := newTestIdempotency(s, &log)
			key := "race-" + time.Now().Format("150405.000000000")
			if _, err := call(i, createThing, key, func(context.Context, any) (any, error) {
				return &testv1.CreateThingResponse{Id: "old"}, nil
			}); err != nil {
				t.Fatal(err)
			}
			expireRecord(t, s, key)

			release := make(chan struct{})
			var runs atomic32
			h := func(context.Context, any) (any, error) {
				runs.add()
				<-release
				return &testv1.CreateThingResponse{Id: "new"}, nil
			}
			type result struct {
				resp any
				err  error
			}
			results := make(chan result, 2)
			for range 2 {
				go func() {
					resp, err := call(i, createThing, key, h)
					results <- result{resp, err}
				}()
			}
			// The loser answers while the winner still runs.
			first := <-results
			if status.Code(first.err) != codes.Aborted {
				t.Errorf("the loser: %v %v", first.resp, first.err)
			}
			close(release)
			second := <-results
			if second.err != nil || second.resp.(*testv1.CreateThingResponse).GetId() != "new" {
				t.Errorf("the winner: %v %v", second.resp, second.err)
			}
			if runs.get() != 1 {
				t.Errorf("the call ran %d times", runs.get())
			}
			// And the next repeat replays the new outcome.
			resp, err := call(i, createThing, key, h)
			if err != nil || resp.(*testv1.CreateThingResponse).GetId() != "new" {
				t.Errorf("after the race: %v %v", resp, err)
			}
		})
	}
}

// A store whose Retake predates expired outcomes — a project's own — keeps the replay
// it had.
type oldRetake struct{ *MemoryIdempotencyStore }

func (s oldRetake) Retake(ctx context.Context, id int64, lockUntil time.Time) (bool, error) {
	s.mu.Lock()
	r := s.byID(id)
	completed := r != nil && r.Status == idemCompleted
	s.mu.Unlock()
	if completed {
		return false, nil
	}
	return s.MemoryIdempotencyStore.Retake(ctx, id, lockUntil)
}

func TestExpiredOutcomeWithAnOlderStore(t *testing.T) {
	var log bytes.Buffer
	store := oldRetake{NewMemoryIdempotencyStore()}
	i := newTestIdempotency(store, &log)
	runs := 0
	h := func(context.Context, any) (any, error) { runs++; return &testv1.CreateThingResponse{Id: "1"}, nil }
	if _, err := call(i, createThing, "k", h); err != nil {
		t.Fatal(err)
	}
	expire(store.MemoryIdempotencyStore)
	if resp, err := call(i, createThing, "k", h); err != nil || resp.(*testv1.CreateThingResponse).GetId() != "1" || runs != 1 {
		t.Errorf("an older store: %v %v, runs %d", resp, err, runs)
	}
}

type atomic32 struct {
	mu sync.Mutex
	n  int
}

func (a *atomic32) add() { a.mu.Lock(); a.n++; a.mu.Unlock() }
func (a *atomic32) get() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.n
}

func pgStore(t *testing.T) IdempotencyStore {
	t.Helper()
	url := os.Getenv("DATABASE_TEST_URL")
	if url == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgdb.Open(ctx, pgdb.Config{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store, err := newPGIdempotencyStore(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func expireRecord(t *testing.T, s IdempotencyStore, key string) {
	t.Helper()
	switch s := s.(type) {
	case *MemoryIdempotencyStore:
		expire(s)
	case *pgIdempotencyStore:
		t.Cleanup(func() {
			_, _ = s.pool.Exec(context.Background(), "DELETE FROM "+idempotencyTable+" WHERE key = $1", key)
		})
		if _, err := s.pool.Exec(context.Background(),
			"UPDATE "+idempotencyTable+" SET expires_at = now() - interval '1 second' WHERE key = $1 AND status = 'completed'", key); err != nil {
			t.Fatal(err)
		}
	}
}

// scripted is a store where another repeat acts between this one's read and its Retake.
type scripted struct {
	*MemoryIdempotencyStore
	meanwhile func(s *MemoryIdempotencyStore, id int64)
}

func (s scripted) Retake(ctx context.Context, id int64, lockUntil time.Time) (bool, error) {
	if s.meanwhile != nil {
		s.meanwhile(s.MemoryIdempotencyStore, id)
	}
	return s.MemoryIdempotencyStore.Retake(ctx, id, lockUntil)
}

// The loser of the race re-reads the record and decides on what it is now.
func TestExpiredOutcomeReread(t *testing.T) {
	for name, tc := range map[string]struct {
		meanwhile func(s *MemoryIdempotencyStore, id int64)
		want      string
		runs      int
	}{
		"the other repeat finished: replay its outcome": {func(s *MemoryIdempotencyStore, id int64) {
			s.mu.Lock()
			defer s.mu.Unlock()
			r := s.byID(id)
			r.Status, r.ExpiresAt, r.Response = idemCompleted, time.Now().Add(time.Hour), mustMarshal(&testv1.CreateThingResponse{Id: "other"})
		}, "other", 0},
		"the record was deleted: hold the key anew": {func(s *MemoryIdempotencyStore, id int64) {
			_, _ = s.DeleteExpired(context.Background(), time.Now())
		}, "mine", 1},
	} {
		t.Run(name, func(t *testing.T) {
			var log bytes.Buffer
			mem := NewMemoryIdempotencyStore()
			store := scripted{MemoryIdempotencyStore: mem}
			i := newTestIdempotency(store, &log)
			if _, err := call(i, createThing, "k", func(context.Context, any) (any, error) {
				return &testv1.CreateThingResponse{Id: "old"}, nil
			}); err != nil {
				t.Fatal(err)
			}
			expire(mem)
			once := sync.Once{}
			store.meanwhile = func(s *MemoryIdempotencyStore, id int64) { once.Do(func() { tc.meanwhile(s, id) }) }
			i.store = store
			runs := 0
			resp, err := call(i, createThing, "k", func(context.Context, any) (any, error) {
				runs++
				return &testv1.CreateThingResponse{Id: "mine"}, nil
			})
			if err != nil || resp.(*testv1.CreateThingResponse).GetId() != tc.want || runs != tc.runs {
				t.Errorf("%v %v, runs %d", resp, err, runs)
			}
			if statusOf(mem) != idemCompleted {
				t.Errorf("status %q", statusOf(mem))
			}
		})
	}
}

func mustMarshal(m proto.Message) []byte {
	b, err := proto.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

// A key the gateway lets through only over gRPC: the project's text for it.
func TestNotPrintableKeyMessage(t *testing.T) {
	var log bytes.Buffer
	i := newTestIdempotency(NewMemoryIdempotencyStore(), &log)
	h := func(context.Context, any) (any, error) { return &testv1.CreateThingResponse{}, nil }
	if _, err := call(i, createThing, "a\tb", h); status.Convert(err).Message() != "Idempotency-Key must be printable ASCII" {
		t.Errorf("the platform's text: %v", err)
	}
	i.texts = IdempotencyKeyMessages{NotPrintable: "bad key"}
	if _, err := call(i, createThing, "a\tb", h); status.Convert(err).Message() != "bad key" {
		t.Errorf("the project's text: %v", err)
	}
}
