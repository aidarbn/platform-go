package rbac_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	testv1 "github.com/aidarbn/platform-go/kit/internal/testapi/platformtest/v1"
	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/modules/rbac"
	"github.com/aidarbn/platform-go/kit/platform"
)

// run starts the api and rbac modules with the role of a call taken from the x-role
// header, and returns the API module, the ops address and the logs.
func run(t *testing.T, modules []platform.Module) (*api.Module, string, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	apiModule := modules[0].(*api.Module)
	wire := func(app *platform.App) error {
		api.Register(app, api.Service{
			GRPC:    func(s *grpc.Server) { testv1.RegisterEchoServiceServer(s, echo{}) },
			Gateway: testv1.RegisterEchoServiceHandler,
		})
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
	t.Cleanup(func() {
		cancel()
		<-errCh
	})
	return apiModule, ops, logs
}

func get(t *testing.T, url, role string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
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

func metrics(t *testing.T, ops string) string {
	t.Helper()
	resp, err := http.Get("http://" + ops + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return string(raw)
}

// A model with role inheritance and a policy in an adapter, given through Configure as a
// generated project does, reloaded on a period while the service runs.
func TestModuleWithAModelAndAnAdapter(t *testing.T) {
	a := &memAdapter{}
	a.set("p, *, /grpc.health.v1.Health/*, *", "p, editor, /platformtest.v1.EchoService/*, *", "g, alice, editor",
		"p, editor, /platformtest.v1.Gone/*, *")
	apiModule := api.New(api.Config{GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	modules := []platform.Module{apiModule, rbac.New(rbac.Config{}, rbac.WithPolicy("p, *, /x/*, *\n"))}
	if err := rbac.Configure(modules, rbac.WithModel(inheritModel), rbac.WithAdapter(a), rbac.WithReloadEvery(20*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	_, ops, logs := run(t, modules)
	url := "http://" + apiModule.HTTPAddr() + "/v1/fail/none"

	if got := get(t, url, "alice"); got != http.StatusOK {
		t.Errorf("alice: %d", got)
	}
	if got := get(t, url, "bob"); got != http.StatusForbidden {
		t.Errorf("bob: %d", got)
	}
	if got := get(t, url, ""); got != http.StatusUnauthorized {
		t.Errorf("no roles: %d", got)
	}

	// bob becomes an editor in the store; the next reload lets him in.
	a.set("p, *, /grpc.health.v1.Health/*, *", "p, editor, /platformtest.v1.EchoService/*, *", "g, bob, editor")
	deadline := time.Now().Add(5 * time.Second)
	for get(t, url, "bob") != http.StatusOK {
		if time.Now().After(deadline) {
			t.Fatal("the policy was not reloaded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := get(t, url, "alice"); got != http.StatusForbidden {
		t.Errorf("alice after the reload: %d", got)
	}

	// A failing store keeps the policy in force, logs and counts.
	a.mu.Lock()
	a.fail = io.ErrUnexpectedEOF
	a.mu.Unlock()
	deadline = time.Now().Add(5 * time.Second)
	for !strings.Contains(metrics(t, ops), `rbac_policy_reloads_total{result="error"}`) {
		if time.Now().After(deadline) {
			t.Fatal("a failed reload is not counted")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := get(t, url, "bob"); got != http.StatusOK {
		t.Errorf("bob after a failed reload: %d", got)
	}
	m := metrics(t, ops)
	for _, want := range []string{`rbac_policy_reloads_total{result="ok"}`, `rbac_denied_total{method="/platformtest.v1.EchoService/Fail",reason="forbidden"}`} {
		if !strings.Contains(m, want) {
			t.Errorf("metrics lack %s", want)
		}
	}
	for _, want := range []string{"the access policy did not reload", "an access rule matches no gRPC method", "platformtest.v1.Gone"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %q:\n%s", want, logs.String())
		}
	}
}

// Without the new options nothing new shows: no reload metric, no reload log.
func TestModuleByDefaultHasNoReloads(t *testing.T) {
	apiModule := api.New(api.Config{GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	_, ops, logs := run(t, []platform.Module{apiModule, rbac.New(rbac.Config{}, rbac.WithPolicy(policy))})
	if got := get(t, "http://"+apiModule.HTTPAddr()+"/v1/fail/none", "admin"); got != http.StatusOK {
		t.Errorf("admin: %d", got)
	}
	if strings.Contains(metrics(t, ops), "rbac_policy_reloads_total") {
		t.Error("the reload metric without an adapter")
	}
	if strings.Contains(logs.String(), "reload") {
		t.Errorf("reload logs without an adapter:\n%s", logs.String())
	}
}

// A request of the project's own that fails without a status answers Internal and is
// logged, over gRPC too.
func TestModuleRequestFailure(t *testing.T) {
	apiModule := api.New(api.Config{GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	modules := []platform.Module{apiModule, rbac.New(rbac.Config{},
		rbac.WithModel(domainModel), rbac.WithPolicy(domainPolicy), rbac.WithRequest(tenantRequest))}
	_, ops, logs := run(t, modules)

	conn, err := grpc.NewClient(apiModule.GRPCAddr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := testv1.NewEchoServiceClient(conn)
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-role", "alice")
	// tenantRequest finds no tenant in the context: InvalidArgument, as it returned.
	if _, err := client.Echo(ctx, &testv1.EchoRequest{MessageText: "hi"}); err == nil || !strings.Contains(err.Error(), "no tenant") {
		t.Errorf("Echo: %v", err)
	}
	if !strings.Contains(metrics(t, ops), `rbac_denied_total{method="/platformtest.v1.EchoService/Echo",reason="error"}`) {
		t.Error("a refused request is not counted")
	}
	_ = logs
}
