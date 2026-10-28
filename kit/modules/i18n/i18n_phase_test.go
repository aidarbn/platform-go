package i18n_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/aidarbn/platform-go/kit/i18nx"
	testv1 "github.com/aidarbn/platform-go/kit/internal/testapi/platformtest/v1"
	"github.com/aidarbn/platform-go/kit/logx"
	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/modules/i18n"
	"github.com/aidarbn/platform-go/kit/modules/postgres"
	"github.com/aidarbn/platform-go/kit/platform"
)

// failing answers Fail with the text the authentication refuses with, from the handler.
type failing struct{ echo }

func (failing) Fail(context.Context, *testv1.FailRequest) (*testv1.FailResponse, error) {
	return nil, status.Error(codes.Unauthenticated, "authentication required")
}

// With the error phase after validation, as taply had it, a refusal of the chain keeps
// its text and an error of the handler is translated.
func TestErrorPhaseAfterValidate(t *testing.T) {
	dsn := database(t)
	apiModule := api.New(api.Config{GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	modules := []platform.Module{
		postgres.New(postgres.Config{URL: dsn}),
		apiModule,
		i18n.New(i18n.Config{DefaultLocale: "ru", Locales: []i18nx.Locale{"ru", "en"}}),
	}
	if err := i18n.Configure(modules, i18n.WithErrorPhase(api.PhaseAfterValidate)); err != nil {
		t.Fatal(err)
	}
	if err := i18n.Configure(modules[:2]); err == nil {
		t.Error("Configure without the module")
	}
	var app *platform.App
	wire := func(a *platform.App) error {
		app = a
		api.Register(a, api.Service{
			GRPC:    func(s *grpc.Server) { testv1.RegisterEchoServiceServer(s, failing{}) },
			Gateway: testv1.RegisterEchoServiceHandler,
		})
		api.AddUnaryInterceptor(a, func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			md, _ := metadata.FromIncomingContext(ctx)
			if len(md.Get("x-user")) == 0 {
				return nil, status.Error(codes.Unauthenticated, "authentication required")
			}
			return h(ctx, req)
		})
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	started, errCh := make(chan string, 1), make(chan error, 1)
	cfg := platform.Config{
		Service: "i18n-phase-test", OpsAddr: "127.0.0.1:0", ShutdownTimeout: 3 * time.Second,
		Logger: logx.New(logx.Options{Writer: io.Discard}), OnStarted: func(a string) { started <- a },
	}
	go func() { errCh <- platform.RunContext(ctx, cfg, modules, wire) }()
	select {
	case <-started:
	case err := <-errCh:
		t.Fatalf("Run: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("did not start")
	}
	defer func() {
		cancel()
		<-errCh
	}()

	chain := strings.Join(api.Chain(app), " ")
	if !strings.Contains(chain, "after-validate/i18n-errors") || strings.Contains(chain, "errors-i18n/") {
		t.Errorf("chain: %s", chain)
	}
	get := func(user string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", "http://"+apiModule.HTTPAddr()+"/v1/fail/none", nil)
		req.Header.Set("Accept-Language", "ru")
		if user != "" {
			req.Header.Set("X-User", user)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	if code, body := get(""); code != http.StatusUnauthorized || !strings.Contains(body, `"authentication required"`) {
		t.Errorf("the refusal of the chain was translated: %d %s", code, body)
	}
	if code, body := get("alice"); code != http.StatusUnauthorized || !strings.Contains(body, "требуется аутентификация") {
		t.Errorf("the error of the handler was not translated: %d %s", code, body)
	}
}
