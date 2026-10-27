package i18n_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/aidarbn/platform-go/kit/i18nx"
	testv1 "github.com/aidarbn/platform-go/kit/internal/testapi/platformtest/v1"
	"github.com/aidarbn/platform-go/kit/logx"
	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/modules/i18n"
	"github.com/aidarbn/platform-go/kit/modules/postgres"
	"github.com/aidarbn/platform-go/kit/platform"
)

type userKey struct{}

// The translation of error messages sits outside the project's authentication, so its
// refusal is translated; the translation of response fields sits inside it, so
// SkipTranslation sees the caller the authentication found.
func TestTranslationAroundAuthentication(t *testing.T) {
	dsn := database(t)
	apiModule := api.New(api.Config{GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	modules := []platform.Module{
		postgres.New(postgres.Config{URL: dsn}),
		apiModule,
		i18n.New(i18n.Config{DefaultLocale: "ru", Locales: []i18nx.Locale{"ru", "en"}}),
	}
	var (
		mu    sync.Mutex
		users []string
		app   *platform.App
	)
	wire := func(a *platform.App) error {
		app = a
		api.Register(a, api.Service{
			GRPC:    func(s *grpc.Server) { testv1.RegisterEchoServiceServer(s, echo{}) },
			Gateway: testv1.RegisterEchoServiceHandler,
		})
		api.AddUnaryInterceptor(a, func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			md, _ := metadata.FromIncomingContext(ctx)
			user := md.Get("x-user")
			if len(user) == 0 {
				return nil, status.Error(codes.Unauthenticated, "authentication required")
			}
			return h(context.WithValue(ctx, userKey{}, user[0]), req)
		})
		api.Normalize(a, func(proto.Message) {})
		i18n.SkipTranslation(a, func(ctx context.Context) bool {
			user, _ := ctx.Value(userKey{}).(string)
			mu.Lock()
			users = append(users, user)
			mu.Unlock()
			return user == "tablet"
		})
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	started, errCh := make(chan string, 1), make(chan error, 1)
	cfg := platform.Config{
		Service: "i18n-chain-test", OpsAddr: "127.0.0.1:0", ShutdownTimeout: 3 * time.Second,
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

	chain := api.Chain(app)
	at := func(entry string) int {
		for i, e := range chain {
			if e == entry || strings.HasPrefix(entry, "authenticate/") && strings.HasPrefix(e, "authenticate/") {
				return i
			}
		}
		t.Fatalf("%s is not in %v", entry, chain)
		return -1
	}
	for _, pair := range [][2]string{
		{"errors-i18n/i18n-errors", "authenticate/"},
		{"authenticate/", "transform/i18n-fields"},
		{"authenticate/", "transform/i18n-base"},
		{"transform/i18n-fields", "validate/validate"},
		{"transform/i18n-base", "transform+50/normalize"},
		{"transform+50/normalize", "validate/validate"},
		{"validate/validate", "after-validate/i18n-mirror"},
	} {
		if at(pair[0]) > at(pair[1]) {
			t.Errorf("%s is not before %s in %v", pair[0], pair[1], chain)
		}
	}

	post := func(user string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("POST", "http://"+apiModule.HTTPAddr()+"/v1/echo", strings.NewReader(`{"message_text":"hi"}`))
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

	if code, body := post(""); code != http.StatusUnauthorized || !strings.Contains(body, "требуется аутентификация") {
		t.Errorf("refusal of the authentication: %d %s", code, body)
	}
	for _, user := range []string{"tablet", "alice"} {
		if code, body := post(user); code != http.StatusOK {
			t.Errorf("%s: %d %s", user, code, body)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(users, ",") != "tablet,alice" {
		t.Errorf("SkipTranslation saw %q, want the caller of the authentication", users)
	}
}
