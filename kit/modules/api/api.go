// Package api is a platform module: a gRPC server and a REST gateway in front of it,
// with the interceptors every service needs and the OpenAPI description of the API.
//
// The project writes proto files and handlers and registers its services from
// wireDomain; the module owns the servers, the interceptor chain, the gateway options,
// health, metrics, CORS and the documentation page.
package api

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"buf.build/go/protovalidate"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/platform"
)

// Config holds module settings. Load fills it from the environment; the platform
// generator writes the Load call into the project's config.gen.go.
type Config struct {
	GRPCAddr    string   // gRPC listen address
	HTTPAddr    string   // REST gateway listen address
	MaxRecvSize int      // largest request message, bytes
	MaxSendSize int      // largest response message, bytes
	CORSOrigins []string // origins allowed to call the REST API from a browser; "*" allows any
	Docs        bool     // serve /openapi.yaml and the /docs page
	Reflection  bool     // gRPC server reflection, for grpcurl and similar tools
}

// Load reads the module settings from environment variables.
func Load(l *confx.Loader) Config {
	return Config{
		GRPCAddr:    l.String("API_GRPC_ADDR", "127.0.0.1:9091"),
		HTTPAddr:    l.String("API_HTTP_ADDR", ":8080"),
		MaxRecvSize: l.Int("API_MAX_RECV_MB", 16) << 20,
		MaxSendSize: l.Int("API_MAX_SEND_MB", 32) << 20,
		CORSOrigins: l.Strings("API_CORS_ORIGINS", nil),
		Docs:        l.Bool("API_DOCS", true),
		Reflection:  l.Bool("API_REFLECTION", false),
	}
}

func (c *Config) setDefaults() {
	if c.GRPCAddr == "" {
		c.GRPCAddr = "127.0.0.1:9091"
	}
	if c.HTTPAddr == "" {
		c.HTTPAddr = ":8080"
	}
	if c.MaxRecvSize <= 0 {
		c.MaxRecvSize = 16 << 20
	}
	if c.MaxSendSize <= 0 {
		c.MaxSendSize = 32 << 20
	}
}

// Service is a gRPC service of the project and, optionally, its REST gateway. Both
// functions are generated: RegisterXServer from protoc-gen-go-grpc and RegisterXHandler
// from protoc-gen-grpc-gateway.
//
//	api.Register(app, api.Service{
//		GRPC:    func(s *grpc.Server) { ordersv1.RegisterOrdersServiceServer(s, handler) },
//		Gateway: ordersv1.RegisterOrdersServiceHandler,
//	})
type Service struct {
	GRPC    func(*grpc.Server)
	Gateway func(ctx context.Context, mux *runtime.ServeMux, conn *grpc.ClientConn) error
}

// Registry holds what the project adds to the API. It lives in the container and is
// filled from wireDomain, before the module starts.
type Registry struct {
	mu         sync.Mutex
	sealed     bool
	services   []Service
	unary      []grpc.UnaryServerInterceptor
	stream     []grpc.StreamServerInterceptor
	routes     []route
	middleware []func(http.Handler) http.Handler
}

type route struct {
	pattern string
	handler http.Handler
}

func (r *Registry) add(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		panic("api: register services, interceptors and routes in wireDomain, before the modules start")
	}
	fn()
}

func (r *Registry) seal() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sealed = true
}

func registry(app *platform.App) *Registry { return platform.Get[*Registry](app) }

// Register adds a gRPC service and its gateway.
func Register(app *platform.App, s Service) {
	if s.GRPC == nil {
		panic("api: a service needs its GRPC registration")
	}
	r := registry(app)
	r.add(func() { r.services = append(r.services, s) })
}

// AddUnaryInterceptor adds a project interceptor, such as authentication. Project
// interceptors run after recovery, metrics and logging and before request validation.
func AddUnaryInterceptor(app *platform.App, i grpc.UnaryServerInterceptor) {
	r := registry(app)
	r.add(func() { r.unary = append(r.unary, i) })
}

// AddStreamInterceptor adds a project stream interceptor.
func AddStreamInterceptor(app *platform.App, i grpc.StreamServerInterceptor) {
	r := registry(app)
	r.add(func() { r.stream = append(r.stream, i) })
}

// HandleHTTP serves a plain HTTP route next to the gateway: webhooks, file downloads,
// anything that is not a gRPC call.
func HandleHTTP(app *platform.App, pattern string, h http.Handler) {
	r := registry(app)
	r.add(func() { r.routes = append(r.routes, route{pattern: pattern, handler: h}) })
}

// UseHTTP wraps the whole REST side in a middleware.
func UseHTTP(app *platform.App, mw func(http.Handler) http.Handler) {
	r := registry(app)
	r.add(func() { r.middleware = append(r.middleware, mw) })
}

// Option configures the module.
type Option func(*Module)

// WithOpenAPI gives the module the generated OpenAPI description: a directory holding
// openapi.yaml. The generated wiring passes the embedded api/openapi directory.
func WithOpenAPI(fsys fs.FS) Option { return func(m *Module) { m.openapi = fsys } }

// Module implements platform.Module.
type Module struct {
	cfg      Config
	openapi  fs.FS
	registry *Registry
	log      *slog.Logger
	metrics  *metrics

	grpcSrv  *grpc.Server
	grpcLn   net.Listener
	httpSrv  *http.Server
	httpLn   net.Listener
	conn     *grpc.ClientConn
	health   *health.Server
	gwCancel context.CancelFunc
}

// New creates the module from ready settings.
func New(cfg Config, opts ...Option) *Module {
	cfg.setDefaults()
	m := &Module{cfg: cfg, registry: &Registry{}}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func (m *Module) Name() string { return "api" }

// Init puts the registry into the container, so wireDomain can register services.
func (m *Module) Init(_ context.Context, app *platform.App) error {
	m.log = app.Logger().With("module", "api")
	met, err := newMetrics(app.Metrics())
	if err != nil {
		return err
	}
	m.metrics = met
	platform.Provide(app, m.registry)
	return nil
}

// Start builds the servers from what the project registered and starts serving.
func (m *Module) Start(ctx context.Context) error {
	m.registry.seal()

	validator, err := protovalidate.New()
	if err != nil {
		return fmt.Errorf("api: request validator: %w", err)
	}

	// Metrics and logging are outermost, so they see a recovered panic and a hidden
	// internal error as the Internal status the client gets.
	unary := slices.Concat(
		[]grpc.UnaryServerInterceptor{m.metrics.unary, logUnary(m.log), recoverUnary(m.log, m.metrics), errorsUnary(m.log)},
		m.registry.unary,
		[]grpc.UnaryServerInterceptor{validateUnary(validator)},
	)
	stream := slices.Concat(
		[]grpc.StreamServerInterceptor{m.metrics.stream, logStream(m.log), recoverStream(m.log, m.metrics)},
		m.registry.stream,
	)

	m.grpcSrv = grpc.NewServer(
		grpc.MaxRecvMsgSize(m.cfg.MaxRecvSize),
		grpc.MaxSendMsgSize(m.cfg.MaxSendSize),
		grpc.ChainUnaryInterceptor(unary...),
		grpc.ChainStreamInterceptor(stream...),
	)
	m.health = health.NewServer()
	healthpb.RegisterHealthServer(m.grpcSrv, m.health)
	if m.cfg.Reflection {
		reflection.Register(m.grpcSrv)
	}
	for _, s := range m.registry.services {
		s.GRPC(m.grpcSrv)
	}

	if m.grpcLn, err = net.Listen("tcp", m.cfg.GRPCAddr); err != nil {
		return fmt.Errorf("api: gRPC on %s: %w", m.cfg.GRPCAddr, err)
	}
	go func() {
		if err := m.grpcSrv.Serve(m.grpcLn); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			m.log.Error("the gRPC server stopped with an error", "err", err)
		}
	}()

	// The gateway calls the local gRPC server over a real connection, so a REST request
	// goes through exactly the same interceptors as a gRPC one.
	m.conn, err = grpc.NewClient(dialAddr(m.grpcLn.Addr()),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(m.cfg.MaxSendSize),
			grpc.MaxCallSendMsgSize(m.cfg.MaxRecvSize),
		),
	)
	if err != nil {
		return fmt.Errorf("api: gateway connection: %w", err)
	}

	gwCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	m.gwCancel = cancel
	gateway := newGateway()
	for _, s := range m.registry.services {
		if s.Gateway == nil {
			continue
		}
		if err := s.Gateway(gwCtx, gateway, m.conn); err != nil {
			return fmt.Errorf("api: register gateway: %w", err)
		}
	}

	handler := m.httpHandler(gateway)
	if m.httpLn, err = net.Listen("tcp", m.cfg.HTTPAddr); err != nil {
		return fmt.Errorf("api: HTTP on %s: %w", m.cfg.HTTPAddr, err)
	}
	m.httpSrv = &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := m.httpSrv.Serve(m.httpLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.log.Error("the HTTP server stopped with an error", "err", err)
		}
	}()

	m.health.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	m.log.Info("the API is listening", "grpc", m.GRPCAddr(), "http", m.HTTPAddr(), "services", len(m.registry.services))
	return nil
}

func (m *Module) httpHandler(gateway *runtime.ServeMux) http.Handler {
	mux := http.NewServeMux()
	for _, r := range m.registry.routes {
		mux.Handle(r.pattern, r.handler)
	}
	if m.cfg.Docs && m.openapi != nil {
		if spec, err := fs.ReadFile(m.openapi, "openapi.yaml"); err == nil {
			mux.HandleFunc("GET /openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/yaml")
				_, _ = w.Write(spec)
			})
			mux.HandleFunc("GET /docs", docsPage)
		}
	}
	mux.Handle("/", gateway)

	var h http.Handler = mux
	for i := len(m.registry.middleware) - 1; i >= 0; i-- {
		h = m.registry.middleware[i](h)
	}
	h = recoverHTTP(m.log, m.metrics, h)
	if len(m.cfg.CORSOrigins) > 0 {
		h = cors(m.cfg.CORSOrigins, h)
	}
	return h
}

// newGateway configures the REST side: snake_case JSON as in the proto files, every
// field present in responses, unknown request fields ignored so old clients keep
// working, and request headers forwarded to the gRPC handlers as metadata.
func newGateway() *runtime.ServeMux {
	return runtime.NewServeMux(
		runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{
			MarshalOptions:   protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true},
			UnmarshalOptions: protojson.UnmarshalOptions{DiscardUnknown: true},
		}),
		runtime.WithIncomingHeaderMatcher(forwardHeader),
	)
}

// hopByHop are headers that describe the HTTP connection, not the call.
var hopByHop = []string{
	"connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer",
	"transfer-encoding", "upgrade", "content-length", "content-type", "host", "accept-encoding",
}

func forwardHeader(key string) (string, bool) {
	lower := strings.ToLower(key)
	if slices.Contains(hopByHop, lower) {
		return "", false
	}
	return lower, true
}

// dialAddr is where the gateway reaches the gRPC server: a server listening on every
// interface is reached over loopback.
func dialAddr(addr net.Addr) string {
	if tcp, ok := addr.(*net.TCPAddr); ok && tcp.IP.IsUnspecified() {
		return fmt.Sprintf("127.0.0.1:%d", tcp.Port)
	}
	return addr.String()
}

// GRPCAddr is the address the gRPC server listens on.
func (m *Module) GRPCAddr() string {
	if m.grpcLn == nil {
		return m.cfg.GRPCAddr
	}
	return m.grpcLn.Addr().String()
}

// HTTPAddr is the address the REST gateway listens on.
func (m *Module) HTTPAddr() string {
	if m.httpLn == nil {
		return m.cfg.HTTPAddr
	}
	return m.httpLn.Addr().String()
}

// Health reports whether both servers are serving.
func (m *Module) Health(context.Context) error {
	if m.grpcLn == nil || m.httpLn == nil {
		return errors.New("the API is not started")
	}
	return nil
}

// Stop stops taking requests and lets the running ones finish within the shutdown
// timeout; what is still running after it is cut off.
func (m *Module) Stop(ctx context.Context) error {
	if m.health != nil {
		m.health.Shutdown()
	}
	var errs []error
	if m.httpSrv != nil {
		errs = append(errs, m.httpSrv.Shutdown(ctx))
	}
	if m.gwCancel != nil {
		m.gwCancel()
	}
	if m.conn != nil {
		errs = append(errs, m.conn.Close())
	}
	if m.grpcSrv != nil {
		done := make(chan struct{})
		go func() {
			m.grpcSrv.GracefulStop()
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
			m.grpcSrv.Stop()
		}
	}
	return errors.Join(errs...)
}
