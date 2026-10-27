package i18n_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/aidarbn/platform-go/kit/i18nx"
	testv1 "github.com/aidarbn/platform-go/kit/internal/testapi/platformtest/v1"
	"github.com/aidarbn/platform-go/kit/logx"
	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/modules/i18n"
	"github.com/aidarbn/platform-go/kit/modules/postgres"
	"github.com/aidarbn/platform-go/kit/platform"
)

type echo struct {
	testv1.UnimplementedEchoServiceServer
}

func (echo) Echo(_ context.Context, req *testv1.EchoRequest) (*testv1.EchoResponse, error) {
	return &testv1.EchoResponse{MessageText: req.GetMessageText()}, nil
}

func TestSkipTranslationPicksCalls(t *testing.T) {
	dsn := database(t)
	apiModule := api.New(api.Config{GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	modules := []platform.Module{
		postgres.New(postgres.Config{URL: dsn}),
		apiModule,
		i18n.New(i18n.Config{DefaultLocale: "ru", Locales: []i18nx.Locale{"ru", "en"}}),
	}
	var (
		skipped atomic.Int32
		app     atomic.Pointer[platform.App]
		client  atomic.Pointer[i18nx.Client]
	)
	wire := func(a *platform.App) error {
		app.Store(a)
		client.Store(i18n.From(a))
		i18n.SkipTranslation(a, func(ctx context.Context) bool {
			skipped.Add(1)
			return true
		})
		api.Register(a, api.Service{
			GRPC:    func(s *grpc.Server) { testv1.RegisterEchoServiceServer(s, echo{}) },
			Gateway: testv1.RegisterEchoServiceHandler,
		})
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	started, errCh := make(chan string, 1), make(chan error, 1)
	cfg := platform.Config{
		Service: "i18n-skip-test", OpsAddr: "127.0.0.1:0", ShutdownTimeout: 3 * time.Second,
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
	if client.Load() == nil {
		t.Fatal("i18n.From returned nil")
	}

	post := func() {
		t.Helper()
		resp, err := http.Post("http://"+apiModule.HTTPAddr()+"/v1/echo", "application/json", strings.NewReader(`{"messageText":"hi"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), "hi") {
			t.Fatalf("%d %s", resp.StatusCode, raw)
		}
	}
	post()
	if skipped.Load() != 1 {
		t.Fatalf("skip called %d times, want 1", skipped.Load())
	}
	// Without a picker every call is translated again.
	i18n.SkipTranslation(app.Load(), nil)
	post()
	if skipped.Load() != 1 {
		t.Fatalf("skip called %d times after removal", skipped.Load())
	}
}

func TestInitNeedsTheDatabase(t *testing.T) {
	err := i18n.New(i18n.Config{}).Init(context.Background(), platform.NewApp(nil))
	if err == nil || !strings.Contains(err.Error(), "postgres module") {
		t.Fatalf("err = %v", err)
	}
}
