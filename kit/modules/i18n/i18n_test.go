package i18n_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/i18nx"
	testv1 "github.com/aidarbn/platform-go/kit/internal/testapi/platformtest/v1"
	"github.com/aidarbn/platform-go/kit/logx"
	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/modules/i18n"
	"github.com/aidarbn/platform-go/kit/modules/postgres"
	"github.com/aidarbn/platform-go/kit/pgdb"
	"github.com/aidarbn/platform-go/kit/platform"
)

func TestLoad(t *testing.T) {
	t.Setenv("I18N_LOCALES", "KK, en")
	t.Setenv("I18N_DEFAULT_LOCALE", "kk")
	l := confx.New("")
	cfg := i18n.Load(l)
	if err := l.Err(); err != nil || cfg.DefaultLocale != "kk" || len(cfg.Locales) != 2 || cfg.Locales[0] != "kk" {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
}

type orders struct {
	testv1.UnimplementedEchoServiceServer
}

func (orders) Fail(context.Context, *testv1.FailRequest) (*testv1.FailResponse, error) {
	return nil, status.Error(codes.NotFound, "order 42: not found")
}

func database(t *testing.T) string {
	t.Helper()
	base := os.Getenv("DATABASE_TEST_URL")
	if base == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgdb.Open(ctx, pgdb.Config{URL: base})
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("i18n_module_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		admin.Close()
	})
	u, _ := url.Parse(base)
	u.Path = "/" + name
	return u.String()
}

func TestModuleTranslatesTheAPI(t *testing.T) {
	dsn := database(t)
	apiModule := api.New(api.Config{GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	messages := "error.resource.order: {en: order, ru: заказ, kk: тапсырыс}\n"
	modules := []platform.Module{
		postgres.New(postgres.Config{URL: dsn}),
		apiModule,
		i18n.New(i18n.Config{DefaultLocale: "ru", Locales: []i18nx.Locale{"ru", "kk", "en"}}, i18n.WithMessages(messages)),
	}
	wire := func(app *platform.App) error {
		api.Register(app, api.Service{
			GRPC:    func(s *grpc.Server) { testv1.RegisterEchoServiceServer(s, orders{}) },
			Gateway: testv1.RegisterEchoServiceHandler,
		})
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	started, errCh := make(chan string, 1), make(chan error, 1)
	cfg := platform.Config{
		Service: "i18n-test", OpsAddr: "127.0.0.1:0", ShutdownTimeout: 3 * time.Second,
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

	get := func(language string) string {
		req, _ := http.NewRequest("GET", "http://"+apiModule.HTTPAddr()+"/v1/fail/x", nil)
		req.Header.Set("Accept-Language", language)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return string(raw)
	}
	// The project's resource name and the platform's "not found" together.
	for language, want := range map[string]string{"kk-KZ": "тапсырыс 42: табылмады", "ru": "заказ 42: не найден", "en": "order 42: not found", "fr": "заказ 42: не найден"} {
		if body := get(language); !strings.Contains(body, want) {
			t.Errorf("%s: %s, want %s", language, body, want)
		}
	}
}

func TestBrokenMessagesStopTheStart(t *testing.T) {
	dsn := database(t)
	ctx := context.Background()
	app := platform.NewApp(nil)
	pg := postgres.New(postgres.Config{URL: dsn})
	if err := pg.Init(ctx, app); err != nil {
		t.Fatal(err)
	}
	defer pg.Stop(ctx)
	if err := api.New(api.Config{}).Init(ctx, app); err != nil {
		t.Fatal(err)
	}
	err := i18n.New(i18n.Config{}, i18n.WithMessages("error.tmpl.x: {ru: \"%s\"}\n")).Init(ctx, app)
	if err == nil || !strings.Contains(err.Error(), "i18n/messages.yaml") {
		t.Fatalf("err = %v", err)
	}
}
