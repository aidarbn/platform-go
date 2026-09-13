package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	testv1 "github.com/aidarbn/platform-go/kit/internal/testapi/platformtest/v1"
	"github.com/aidarbn/platform-go/kit/logx"
	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/platform"
)

// extraWire lets a test add registrations to the shared test service.
var extraWire func(app *platform.App)

// echo is the project handler of the test service.
type echo struct {
	testv1.UnimplementedEchoServiceServer
}

func (echo) Echo(ctx context.Context, req *testv1.EchoRequest) (*testv1.EchoResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	return &testv1.EchoResponse{
		MessageText: req.GetMessageText(),
		Length:      int32(len(req.GetMessageText())),
		Shouted:     len(md.Get("x-shout")) > 0,
	}, nil
}

func (echo) Fail(ctx context.Context, req *testv1.FailRequest) (*testv1.FailResponse, error) {
	switch req.GetKind() {
	case "panic":
		panic("the handler broke")
	case "internal":
		return nil, errors.New("pq: password authentication failed for user secret")
	case "not_found":
		return nil, status.Error(codes.NotFound, "no such order")
	case "unavailable":
		return nil, status.Error(codes.Unavailable, "the payment system is down")
	case "slow":
		select {
		case <-time.After(2 * time.Second):
			return &testv1.FailResponse{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &testv1.FailResponse{}, nil
}

type service struct {
	t       *testing.T
	logs    *syncBuffer
	module  *api.Module
	ops     string
	seen    chan string
	stopRun context.CancelFunc
	errCh   chan error
}

func start(t *testing.T, cfg api.Config, opts ...api.Option) *service {
	t.Helper()

	cfg.GRPCAddr, cfg.HTTPAddr = "127.0.0.1:0", "127.0.0.1:0"
	module := api.New(cfg, opts...)
	s := &service{t: t, module: module, seen: make(chan string, 16), errCh: make(chan error, 1), logs: &syncBuffer{}}

	wire := func(app *platform.App) error {
		api.Register(app, api.Service{
			GRPC:    func(g *grpc.Server) { testv1.RegisterEchoServiceServer(g, echo{}) },
			Gateway: testv1.RegisterEchoServiceHandler,
		})
		// A project interceptor, such as authentication, sees REST calls as gRPC calls.
		api.AddUnaryInterceptor(app, func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			md, _ := metadata.FromIncomingContext(ctx)
			s.seen <- info.FullMethod + " user=" + strings.Join(md.Get("x-user"), ",")
			return h(ctx, req)
		})
		api.HandleHTTP(app, "GET /webhooks/ping", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("pong"))
		}))
		if extraWire != nil {
			extraWire(app)
		}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	s.stopRun = cancel
	started := make(chan string, 1)
	cfgRun := platform.Config{
		Service: "echo", OpsAddr: "127.0.0.1:0", ShutdownTimeout: 3 * time.Second,
		Logger:    logx.New(logx.Options{Writer: s.logs}),
		OnStarted: func(addr string) { started <- addr },
	}
	go func() { s.errCh <- platform.RunContext(ctx, cfgRun, []platform.Module{module}, wire) }()

	select {
	case s.ops = <-started:
	case err := <-s.errCh:
		t.Fatalf("Run returned before startup: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("the service did not start")
	}
	t.Cleanup(s.stop)
	return s
}

func (s *service) stop() {
	s.stopRun()
	select {
	case err := <-s.errCh:
		if err != nil {
			s.t.Errorf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		s.t.Error("Run did not return")
	}
}

func (s *service) do(method, path, body string, header map[string]string) (int, string, http.Header) {
	s.t.Helper()
	req, err := http.NewRequest(method, "http://"+s.module.HTTPAddr()+path, strings.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw), resp.Header
}

func TestRESTCallGoesThroughGRPC(t *testing.T) {
	s := start(t, api.Config{})

	code, body, _ := s.do("POST", "/v1/echo", `{"message_text":"hello","unknown_field":1}`, map[string]string{"X-User": "aidar"})
	if code != http.StatusOK {
		t.Fatalf("code = %d\n%s", code, body)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	// snake_case as in the proto file, and a false field is present rather than dropped.
	if resp["message_text"] != "hello" || resp["length"] != float64(5) || resp["shouted"] != false {
		t.Errorf("response = %s", body)
	}

	select {
	case got := <-s.seen:
		if got != "/platformtest.v1.EchoService/Echo user=aidar" {
			t.Errorf("the project interceptor saw %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("the project interceptor did not run for a REST call")
	}
}

func TestValidationRejectsBadRequest(t *testing.T) {
	s := start(t, api.Config{})

	code, body, _ := s.do("POST", "/v1/echo", `{"message_text":""}`, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("code = %d\n%s", code, body)
	}
	if !strings.Contains(body, "message_text") || !strings.Contains(body, "buf.validate.Violations") {
		t.Errorf("the violations are missing from the answer:\n%s", body)
	}
}

func TestErrors(t *testing.T) {
	s := start(t, api.Config{})

	// A panic answers 500 and the service keeps working.
	code, body, _ := s.do("GET", "/v1/fail/panic", "", nil)
	if code != http.StatusInternalServerError || strings.Contains(body, "the handler broke") {
		t.Errorf("panic: %d %s", code, body)
	}
	if code, _, _ := s.do("POST", "/v1/echo", `{"message_text":"still here"}`, nil); code != http.StatusOK {
		t.Errorf("after a panic: %d", code)
	}

	// A plain error must not leak its text: it can hold credentials or SQL.
	code, body, _ = s.do("GET", "/v1/fail/internal", "", nil)
	if code != http.StatusInternalServerError || strings.Contains(body, "password") {
		t.Errorf("internal: %d %s", code, body)
	}

	// A status error keeps its code and message.
	code, body, _ = s.do("GET", "/v1/fail/not_found", "", nil)
	if code != http.StatusNotFound || !strings.Contains(body, "no such order") {
		t.Errorf("not found: %d %s", code, body)
	}

	// An unknown route answers 404, not a panic or a hang.
	if code, _, _ := s.do("GET", "/v1/nothing", "", nil); code != http.StatusNotFound {
		t.Errorf("unknown route: %d", code)
	}

	metrics := scrape(t, s.ops)
	for _, want := range []string{
		`grpc_req_panics_recovered_total 1`,
		`grpc_server_handled_total{grpc_code="NotFound",grpc_method="Fail",grpc_service="platformtest.v1.EchoService",grpc_type="unary"} 1`,
		`grpc_server_handled_total{grpc_code="Internal",grpc_method="Fail",grpc_service="platformtest.v1.EchoService",grpc_type="unary"} 2`,
	} {
		if !strings.Contains(metrics, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
}

func TestGRPCClientAndHealth(t *testing.T) {
	s := start(t, api.Config{})

	conn, err := grpc.NewClient(s.module.GRPCAddr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-shout", "yes")

	resp, err := testv1.NewEchoServiceClient(conn).Echo(ctx, &testv1.EchoRequest{MessageText: "grpc"})
	if err != nil || resp.GetLength() != 4 || !resp.GetShouted() {
		t.Fatalf("Echo = %v, %v", resp, err)
	}
	_, err = testv1.NewEchoServiceClient(conn).Echo(ctx, &testv1.EchoRequest{MessageText: strings.Repeat("x", 21)})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("a too long message: %v", err)
	}

	h, err := healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil || h.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("health = %v, %v", h, err)
	}

	// The platform /health includes the module.
	resp2, err := http.Get("http://" + s.ops + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	raw, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(raw), `"api":"ok"`) {
		t.Errorf("/health = %s", raw)
	}
}

func TestPlainRouteDocsAndCORS(t *testing.T) {
	spec := fstest.MapFS{
		"openapi.yaml":   {Data: []byte("openapi: 3.1.0\ninfo: {title: Echo, version: 1.0.0}\n")},
		"openapi.gen.go": {Data: []byte("package openapi")},
	}
	s := start(t, api.Config{Docs: true, CORSOrigins: []string{"https://app.example.com"}}, api.WithOpenAPI(spec))

	if code, body, _ := s.do("GET", "/webhooks/ping", "", nil); code != http.StatusOK || body != "pong" {
		t.Errorf("plain route: %d %s", code, body)
	}
	if code, body, _ := s.do("GET", "/openapi.yaml", "", nil); code != http.StatusOK || !strings.Contains(body, "openapi: 3.1.0") {
		t.Errorf("openapi.yaml: %d %s", code, body)
	}
	if code, body, _ := s.do("GET", "/docs", "", nil); code != http.StatusOK || !strings.Contains(body, "/openapi.yaml") {
		t.Errorf("docs: %d %s", code, body)
	}

	code, _, h := s.do("OPTIONS", "/v1/echo", "", map[string]string{
		"Origin": "https://app.example.com", "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "content-type",
	})
	if code != http.StatusNoContent || h.Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Errorf("preflight: %d %v", code, h)
	}
	_, _, h = s.do("POST", "/v1/echo", `{"message_text":"x"}`, map[string]string{"Origin": "https://evil.example.com"})
	if h.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("a foreign origin was allowed: %v", h)
	}
}

func TestDocsOffByDefaultWithoutSpec(t *testing.T) {
	s := start(t, api.Config{Docs: true})
	if code, _, _ := s.do("GET", "/openapi.yaml", "", nil); code != http.StatusNotFound {
		t.Errorf("openapi.yaml without a spec: %d", code)
	}
}

// Stopping lets a running call finish instead of cutting it off.
func TestGracefulStop(t *testing.T) {
	s := start(t, api.Config{})

	done := make(chan int, 1)
	go func() {
		code, _, _ := s.do("GET", "/v1/fail/slow", "", nil)
		done <- code
	}()
	time.Sleep(300 * time.Millisecond)
	s.stopRun()

	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Errorf("the running call ended with %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the running call did not finish")
	}
}

func TestRegisterAfterStartPanics(t *testing.T) {
	app := platform.NewApp(nil)
	m := api.New(api.Config{GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	if err := m.Init(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer m.Stop(context.Background())

	defer func() {
		if recover() == nil {
			t.Fatal("a late registration was accepted")
		}
	}()
	api.HandleHTTP(app, "GET /late", http.NotFoundHandler())
}

func scrape(t *testing.T, ops string) string {
	t.Helper()
	resp, err := http.Get("http://" + ops + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return string(raw)
}

// A worker process registers the same services but serves no API.
func TestWorkerRoleServesNothing(t *testing.T) {
	m := api.New(api.Config{GRPCAddr: "127.0.0.1:0", HTTPAddr: "127.0.0.1:0"})
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan string, 1)
	errCh := make(chan error, 1)
	cfg := platform.Config{
		Service: "worker", Role: platform.RoleWorker, OpsAddr: "127.0.0.1:0", ShutdownTimeout: time.Second,
		Logger: logx.New(logx.Options{Writer: io.Discard}), OnStarted: func(a string) { started <- a },
	}
	wire := func(app *platform.App) error {
		api.Register(app, api.Service{GRPC: func(g *grpc.Server) { testv1.RegisterEchoServiceServer(g, echo{}) }, Gateway: testv1.RegisterEchoServiceHandler})
		return nil
	}
	go func() { errCh <- platform.RunContext(ctx, cfg, []platform.Module{m}, wire) }()

	var ops string
	select {
	case ops = <-started:
	case err := <-errCh:
		t.Fatalf("Run: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("did not start")
	}
	if m.HTTPAddr() != "127.0.0.1:0" {
		t.Errorf("a worker listens for the API on %s", m.HTTPAddr())
	}
	resp, err := http.Get("http://" + ops + "/health")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"api":"ok"`) {
		t.Errorf("health of a worker = %d %s", resp.StatusCode, raw)
	}
	cancel()
	if err := <-errCh; err != nil {
		t.Errorf("Run: %v", err)
	}
}
