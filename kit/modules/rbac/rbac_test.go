package rbac_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	testv1 "github.com/aidarbn/platform-go/kit/internal/testapi/platformtest/v1"
	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/modules/rbac"
	"github.com/aidarbn/platform-go/kit/platform"
)

const policy = `# taply's format
p, *, /grpc.health.v1.Health/*, *
p, support, /platformtest.v1.EchoService/Echo, *
p, admin, /platformtest.v1.EchoService/*, *
p, admin, /platformtest.v1.RemovedService/*, *
`

func TestParsePolicy(t *testing.T) {
	rules, err := rbac.ParsePolicy(policy)
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
	}
	if len(rules) != 4 || rules[1].Role != "support" || rules[1].Line != 3 {
		t.Errorf("rules = %+v", rules)
	}

	_, err = rbac.ParsePolicy("p, admin, /x.v1.S/*, *\ng, admin, root\np, admin, orders, *\np, , /x/*, *\n")
	if err == nil {
		t.Fatal("a broken policy was accepted")
	}
	for _, want := range []string{"line 2", "line 3: method \"orders\" must start with /", "line 4"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err lacks %q: %v", want, err)
		}
	}
}

func TestEnforcer(t *testing.T) {
	e, err := rbac.NewEnforcer(policy)
	if err != nil {
		t.Fatal(err)
	}
	echo, fail := "/platformtest.v1.EchoService/Echo", "/platformtest.v1.EchoService/Fail"

	if !e.Public("/grpc.health.v1.Health/Check") || e.Public(echo) {
		t.Error("public methods are wrong")
	}
	if !e.Allowed([]string{"support"}, echo) || e.Allowed([]string{"support"}, fail) {
		t.Error("support may call Echo only")
	}
	if !e.Allowed([]string{"guest", "admin"}, fail) {
		t.Error("any matching role must be enough")
	}
	if e.Allowed(nil, echo) || e.Allowed([]string{"guest"}, echo) {
		t.Error("an unknown role got access")
	}
	// Claiming the public role does not open private methods.
	if e.Allowed([]string{"*"}, fail) {
		t.Error("the public role opened a private method")
	}

	unmatched := e.Unmatched([]string{echo, fail, "/grpc.health.v1.Health/Check"})
	if len(unmatched) != 1 || unmatched[0].Method != "/platformtest.v1.RemovedService/*" {
		t.Errorf("unmatched = %+v", unmatched)
	}
}

type echo struct {
	testv1.UnimplementedEchoServiceServer
}

func (echo) Echo(_ context.Context, req *testv1.EchoRequest) (*testv1.EchoResponse, error) {
	return &testv1.EchoResponse{MessageText: req.GetMessageText()}, nil
}

func (echo) Fail(context.Context, *testv1.FailRequest) (*testv1.FailResponse, error) {
	return &testv1.FailResponse{}, nil
}

// syncBuffer collects log output from several goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// The access check sits between the project's authentication and the handler, for gRPC
// and REST alike.
func TestAccessThroughTheAPI(t *testing.T) {
	logs := &syncBuffer{}
	apiModule := api.New(api.Config{GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	modules := []platform.Module{apiModule, rbac.New(rbac.Config{}, rbac.WithPolicy(policy))}

	wire := func(app *platform.App) error {
		api.Register(app, api.Service{
			GRPC:    func(s *grpc.Server) { testv1.RegisterEchoServiceServer(s, echo{}) },
			Gateway: testv1.RegisterEchoServiceHandler,
		})
		// The project's authentication: here a header names the role. A real project
		// validates a token, and skips that for public methods.
		api.AddUnaryInterceptor(app, func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			if rbac.IsPublic(app, info.FullMethod) {
				return h(ctx, req)
			}
			md, _ := metadata.FromIncomingContext(ctx)
			if roles := md.Get("x-role"); len(roles) > 0 {
				ctx = rbac.WithRoles(ctx, roles...)
			}
			return h(ctx, req)
		})
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	started, errCh := make(chan string, 1), make(chan error, 1)
	cfg := platform.Config{
		Service: "rbac-test", OpsAddr: "127.0.0.1:0", ShutdownTimeout: 3 * time.Second,
		Logger:    slog.New(slog.NewJSONHandler(logs, nil)),
		OnStarted: func(a string) { started <- a },
	}
	go func() { errCh <- platform.RunContext(ctx, cfg, modules, wire) }()
	var ops string
	select {
	case ops = <-started:
	case err := <-errCh:
		t.Fatalf("Run: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("did not start")
	}
	defer func() {
		cancel()
		<-errCh
	}()

	rest := func(method, path, role string) int {
		req, _ := http.NewRequest(method, "http://"+apiModule.HTTPAddr()+path, strings.NewReader(`{"message_text":"hi"}`))
		if role != "" {
			req.Header.Set("X-Role", role)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	for name, tc := range map[string]struct {
		method, path, role string
		want               int
	}{
		"no roles":          {"POST", "/v1/echo", "", http.StatusUnauthorized},
		"unknown role":      {"POST", "/v1/echo", "guest", http.StatusForbidden},
		"support may echo":  {"POST", "/v1/echo", "support", http.StatusOK},
		"support may not":   {"GET", "/v1/fail/none", "support", http.StatusForbidden},
		"admin may":         {"GET", "/v1/fail/none", "admin", http.StatusOK},
		"public role claim": {"GET", "/v1/fail/none", "*", http.StatusForbidden},
	} {
		if got := rest(tc.method, tc.path, tc.role); got != tc.want {
			t.Errorf("%s: %d, want %d", name, got, tc.want)
		}
	}

	conn, err := grpc.NewClient(apiModule.GRPCAddr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := testv1.NewEchoServiceClient(conn)
	_, err = client.Echo(context.Background(), &testv1.EchoRequest{MessageText: "hi"})
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("gRPC without roles: %v", err)
	}
	withRole := metadata.AppendToOutgoingContext(context.Background(), "x-role", "support")
	if _, err := client.Echo(withRole, &testv1.EchoRequest{MessageText: "hi"}); err != nil {
		t.Errorf("gRPC as support: %v", err)
	}
	_, err = client.Fail(withRole, &testv1.FailRequest{})
	if st, _ := status.FromError(err); st.Code() != codes.PermissionDenied || !strings.Contains(st.Message(), "insufficient role") {
		t.Errorf("gRPC Fail as support: %v", err)
	}

	// A rule for a service that is not registered is reported at start.
	if !strings.Contains(logs.String(), "an access rule matches no gRPC method") || !strings.Contains(logs.String(), "RemovedService") {
		t.Errorf("no warning about the unmatched rule:\n%s", logs.String())
	}

	resp, err := http.Get("http://" + ops + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(raw), `rbac_denied_total{method="/platformtest.v1.EchoService/Echo",reason="unauthenticated"}`) {
		t.Error("denied calls are not counted")
	}
}

func TestBrokenPolicyStopsTheStart(t *testing.T) {
	app := platform.NewApp(nil)
	err := rbac.New(rbac.Config{}, rbac.WithPolicy("p, admin, orders, *\n")).Init(context.Background(), app)
	if err == nil || !strings.Contains(err.Error(), "must start with /") {
		t.Fatalf("err = %v", err)
	}
}
