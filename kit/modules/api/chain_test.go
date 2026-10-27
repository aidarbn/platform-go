package api_test

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"

	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/platform"
)

// unnamed hides the generated names of closures in this package, so the golden lists do
// not depend on how the compiler numbers them.
var unnamed = regexp.MustCompile(`/api_test\.[^ ]*$`)

func normalize(chain []string) string {
	out := make([]string, len(chain))
	for i, entry := range chain {
		out[i] = unnamed.ReplaceAllString(entry, "/<func>")
	}
	return strings.Join(out, "\n")
}

type principalKey struct{}

// The chain is ordered by phase, whatever order the project adds its interceptors in,
// and within a phase the platform's come first and the project's keep their order.
func TestChainOrderByPhase(t *testing.T) {
	var (
		mu    sync.Mutex
		order []string
		app   *platform.App
	)
	record := func(name string) grpc.UnaryServerInterceptor {
		return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			if !strings.HasSuffix(info.FullMethod, "/Echo") {
				return h(ctx, req)
			}
			mu.Lock()
			who, _ := ctx.Value(principalKey{}).(string)
			order = append(order, name+":"+who)
			mu.Unlock()
			return h(ctx, req)
		}
	}
	extraWire = func(a *platform.App) {
		app = a
		// Added in reverse, to show the phases decide.
		api.AddUnaryInterceptorAt(a, api.PhaseAfterValidate, "after", record("after"))
		api.AddUnaryInterceptorAt(a, api.PhaseGuard+10, "late-guard", record("late-guard"))
		api.AddUnaryInterceptorAt(a, api.PhaseGuard, "tenant", record("tenant"))
		api.Authorize(a, record("authorize"), nil)
		api.AddUnaryInterceptorAt(a, api.PhaseAuthenticate, "auth", func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			return record("auth")(context.WithValue(ctx, principalKey{}, "alice"), req, info, h)
		})
		api.AddStreamInterceptorAt(a, api.PhaseGuard, "stream-guard", func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
			return h(srv, ss)
		})
	}
	t.Cleanup(func() { extraWire = nil })
	s := start(t, api.Config{RateLimit: api.RateLimit{RPS: 1000, Burst: 1000}}, api.WithIdempotencyStore(api.NewMemoryIdempotencyStore()))

	want := strings.Join([]string{
		"recover/recover",
		"observe/client-ip",
		"observe/metrics",
		"observe/log",
		"observe/errors",
		"limit/rate-limit",
		"limit/deprecated",
		"authenticate/test-auth", // the interceptor of the test service
		"authenticate/auth",
		"authorize/<func>",
		"guard/tenant",
		"guard+10/late-guard",
		"validate/validate",
		"after-validate/after",
		"idempotency/idempotency",
		"handler-recover/recover",
	}, "\n")
	if got := normalize(api.Chain(app)); got != want {
		t.Errorf("unary chain:\n%s\nwant:\n%s", got, want)
	}
	wantStream := strings.Join([]string{
		"recover/recover",
		"observe/client-ip",
		"observe/metrics",
		"observe/log",
		"guard/stream-guard",
		"handler-recover/recover",
	}, "\n")
	if got := normalize(api.StreamChain(app)); got != wantStream {
		t.Errorf("stream chain:\n%s\nwant:\n%s", got, wantStream)
	}
	if !strings.Contains(s.logs.String(), "the interceptor chain") {
		t.Error("the chain is not logged at start")
	}

	// A valid call runs the phases in order, and every phase after authentication sees
	// the caller it put into the context.
	if code, body, _ := s.do("POST", "/v1/echo", `{"message_text":"hi"}`, nil); code != http.StatusOK {
		t.Fatalf("echo: %d %s", code, body)
	}
	mu.Lock()
	got := strings.Join(order, " ")
	order = nil
	mu.Unlock()
	if want := "auth:alice authorize:alice tenant:alice late-guard:alice after:alice"; got != want {
		t.Errorf("order = %q, want %q", got, want)
	}

	// An invalid request stops at validation: what comes after it never sees it.
	if code, _, _ := s.do("POST", "/v1/echo", `{"message_text":""}`, nil); code != http.StatusBadRequest {
		t.Fatalf("invalid echo: %d", code)
	}
	mu.Lock()
	got = strings.Join(order, " ")
	mu.Unlock()
	if want := "auth:alice authorize:alice tenant:alice late-guard:alice"; got != want {
		t.Errorf("order of an invalid call = %q, want %q", got, want)
	}
}

// A panic of the handler with an Idempotency-Key is remembered as the Internal the
// client got: before the inner recovery it passed through idempotency, the key stayed in
// progress, and a repeat was told the call was still running.
func TestHandlerPanicWithIdempotencyKey(t *testing.T) {
	s := start(t, api.Config{}, api.WithIdempotencyStore(api.NewMemoryIdempotencyStore()))
	key := map[string]string{"Idempotency-Key": "panic-1"}

	first, body, _ := s.do("GET", "/v1/fail/panic", "", key)
	if first != http.StatusInternalServerError || strings.Contains(body, "the handler broke") {
		t.Fatalf("first: %d %s", first, body)
	}
	second, body, _ := s.do("GET", "/v1/fail/panic", "", key)
	if second != http.StatusInternalServerError {
		t.Errorf("repeat: %d %s, want the remembered 500", second, body)
	}
	if metrics := scrape(t, s.ops); !strings.Contains(metrics, "\ngrpc_req_panics_recovered_total 1\n") {
		t.Error("the handler ran again, or its panic was not counted once")
	}
	if !strings.Contains(scrape(t, s.ops), `grpc_server_handled_total{grpc_code="Internal",grpc_method="Fail"`) {
		t.Error("the metrics do not see the panic as Internal")
	}
}

// A panic in a project interceptor is caught by the outer recovery.
func TestInterceptorPanicIsRecovered(t *testing.T) {
	extraWire = func(a *platform.App) {
		api.AddUnaryInterceptorAt(a, api.PhaseGuard, "broken", func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			if strings.HasSuffix(info.FullMethod, "/OldEcho") {
				panic("the guard broke")
			}
			return h(ctx, req)
		})
	}
	t.Cleanup(func() { extraWire = nil })
	s := start(t, api.Config{})
	if code, body, _ := s.do("POST", "/v1/old-echo", `{"message_text":"x"}`, nil); code != http.StatusInternalServerError || strings.Contains(body, "the guard broke") {
		t.Errorf("panic in an interceptor: %d %s", code, body)
	}
	if code, _, _ := s.do("POST", "/v1/echo", `{"message_text":"x"}`, nil); code != http.StatusOK {
		t.Errorf("the service did not survive: %d", code)
	}
	if !strings.Contains(s.logs.String(), "the guard broke") {
		t.Error("the panic is not logged")
	}
	// The outer recovery is outside metrics and logging: the panic is counted on its own,
	// the call is not among the handled ones and has no line of its own.
	metrics := scrape(t, s.ops)
	if !strings.Contains(metrics, "\ngrpc_req_panics_recovered_total 1\n") {
		t.Error("the panic is not counted once")
	}
	for _, line := range strings.Split(metrics, "\n") {
		if strings.HasPrefix(line, "grpc_server_handled_total{") && strings.Contains(line, `grpc_code="Internal",grpc_method="OldEcho"`) && !strings.HasSuffix(line, " 0") {
			t.Errorf("a panic outside the metrics is counted as handled: %s", line)
		}
	}
	for _, line := range strings.Split(s.logs.String(), "\n") {
		if strings.Contains(line, "call failed") && strings.Contains(line, "OldEcho") {
			t.Errorf("a call failed line for the recovered panic: %s", line)
		}
	}
}

func TestPhaseBeforeRecoveryPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("no panic")
		}
	}()
	api.AddUnaryInterceptorAt(nil, api.PhaseRecover-1, "x", func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		return h(ctx, req)
	})
}

// The chain is fixed once the module starts.
func TestAddAtAfterStartPanics(t *testing.T) {
	var app *platform.App
	extraWire = func(a *platform.App) { app = a }
	t.Cleanup(func() { extraWire = nil })
	start(t, api.Config{})
	for name, add := range map[string]func(){
		"unary": func() {
			api.AddUnaryInterceptorAt(app, api.PhaseGuard, "late", func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
				return h(ctx, req)
			})
		},
		"stream": func() {
			api.AddStreamInterceptorAt(app, api.PhaseGuard, "late", func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
				return h(srv, ss)
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("no panic")
				}
			}()
			add()
		})
	}
}

func TestPhaseNames(t *testing.T) {
	for p, want := range map[api.Phase]string{
		api.PhaseRecover:              "recover",
		api.PhaseErrorsI18n:           "errors-i18n",
		api.PhaseHandlerRecover:       "handler-recover",
		api.PhaseGuard + 10:           "guard+10",
		api.PhaseHandlerRecover + 99:  "handler-recover+99",
		api.PhaseHandlerRecover + 100: "phase(1200)",
		-5:                            "phase(-5)",
	} {
		if got := p.String(); got != want {
			t.Errorf("%d: %q, want %q", int(p), got, want)
		}
	}
}

func TestNilInterceptorPanics(t *testing.T) {
	for name, add := range map[string]func(*platform.App){
		"unary":  func(a *platform.App) { api.AddUnaryInterceptorAt(a, api.PhaseGuard, "x", nil) },
		"stream": func(a *platform.App) { api.AddStreamInterceptorAt(a, api.PhaseGuard, "x", nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("no panic")
				}
			}()
			add(nil)
		})
	}
}
