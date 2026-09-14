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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
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

	// RateLimit per client address; zero rates turn it off.
	RateLimit RateLimit

	// Idempotency: how long a finished call is remembered, and how long a running one
	// holds its key before a repeat may take over.
	IdempotencyRetention time.Duration
	IdempotencyLock      time.Duration

	AccessLog       bool // log every HTTP request, as taply does
	LogBodies       bool // put the bodies of failed requests into the access log
	SecurityHeaders bool // taply's security response headers
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

		RateLimit: RateLimit{
			RPS:         float64(l.Int("API_RATE_RPS", 50)),
			Burst:       l.Int("API_RATE_BURST", 100),
			PublicRPS:   float64(l.Int("API_PUBLIC_RATE_RPS", 30)),
			PublicBurst: l.Int("API_PUBLIC_RATE_BURST", 60),
		},
		IdempotencyRetention: l.Duration("API_IDEMPOTENCY_RETENTION", 24*time.Hour),
		IdempotencyLock:      l.Duration("API_IDEMPOTENCY_LOCK", time.Minute),

		AccessLog:       l.Bool("API_ACCESS_LOG", true),
		LogBodies:       l.Bool("API_LOG_BODIES", true),
		SecurityHeaders: l.Bool("API_SECURITY_HEADERS", true),
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
	if c.IdempotencyRetention <= 0 {
		c.IdempotencyRetention = 24 * time.Hour
	}
	if c.IdempotencyLock <= 0 {
		c.IdempotencyLock = time.Minute
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
	authUnary  []grpc.UnaryServerInterceptor
	authStream []grpc.StreamServerInterceptor
	methods    []string

	public       func(method string) bool
	idemRequired []string
	idemUser     func(context.Context) string
	routes       []route
	middleware   []func(http.Handler) http.Handler
	errorHandler runtime.ErrorHandlerFunc
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

// Authorize adds an access check. It runs after every project interceptor — so the
// caller's identity is already in the context — and before request validation. The rbac
// module registers itself here.
func Authorize(app *platform.App, unary grpc.UnaryServerInterceptor, stream grpc.StreamServerInterceptor) {
	r := registry(app)
	r.add(func() {
		if unary != nil {
			r.authUnary = append(r.authUnary, unary)
		}
		if stream != nil {
			r.authStream = append(r.authStream, stream)
		}
	})
}

// PublicMethods tells the API which methods are public, for their separate rate limit.
// The rbac module sets it from its policy.
func PublicMethods(app *platform.App, public func(method string) bool) {
	r := registry(app)
	r.add(func() { r.public = public })
}

// RequireIdempotency makes the methods refuse a call without an Idempotency-Key header,
// as taply does for every method that creates something.
func RequireIdempotency(app *platform.App, methods ...string) {
	r := registry(app)
	r.add(func() { r.idemRequired = append(r.idemRequired, methods...) })
}

// IdempotencyUser scopes idempotency keys to the caller: two users may use the same key.
// The project's authentication supplies the user.
func IdempotencyUser(app *platform.App, user func(ctx context.Context) string) {
	r := registry(app)
	r.add(func() { r.idemUser = user })
}

// Methods returns the full names of every registered gRPC method, such as
// /shop.v1.OrdersService/CreateOrder. It is filled when the module starts.
func Methods(app *platform.App) []string {
	r := registry(app)
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.methods)
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

// HTTPErrorHandler replaces how the REST side writes an error, for an API whose contract
// has its own error body. It receives every error of the gateway: from the handlers,
// from request decoding and routing, and a body over the limit as a
// *runtime.HTTPStatusError with 413. A later call replaces an earlier one.
func HTTPErrorHandler(app *platform.App, h runtime.ErrorHandlerFunc) {
	r := registry(app)
	r.add(func() { r.errorHandler = h })
}

// Option configures the module.
type Option func(*Module)

// WithIdempotencyStore sets where idempotency records are kept. Without it the module
// uses the database of the postgres module; tests pass a memory store.
func WithIdempotencyStore(store IdempotencyStore) Option {
	return func(m *Module) { m.idemStore = store }
}

// WithOpenAPI gives the module the generated OpenAPI description: a directory holding
// openapi.yaml. The generated wiring passes the embedded api/openapi directory.
func WithOpenAPI(fsys fs.FS) Option { return func(m *Module) { m.openapi = fsys } }

// Module implements platform.Module.
type Module struct {
	cfg     Config
	skipped bool
	http    *httpMetrics
	app     *platform.App

	idemStore  IdempotencyStore
	limiter    *rateLimiter
	deprecated *prometheus.CounterVec
	cleanup    chan struct{}
	openapi    fs.FS
	registry   *Registry
	log        *slog.Logger
	metrics    *metrics

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
	if m.http, err = newHTTPMetrics(app.Metrics()); err != nil {
		return err
	}
	m.skipped = !app.Serves(platform.RoleAPI)
	m.app = app
	m.deprecated = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "api", Name: "deprecated_calls_total", Help: "calls to methods marked deprecated in the proto files",
	}, []string{"method"})
	if err := app.Metrics().Register(m.deprecated); err != nil {
		return fmt.Errorf("api metrics: %w", err)
	}
	platform.Provide(app, m.registry)
	return nil
}

// Start builds the servers from what the project registered and starts serving.
func (m *Module) Start(ctx context.Context) error {
	m.registry.seal()
	// A worker process does not serve the API; services are still registered, so the
	// same wireDomain works for every role.
	if m.skipped {
		m.log.Info("the API is not served in this role")
		return nil
	}

	validator, err := protovalidate.New()
	if err != nil {
		return fmt.Errorf("api: request validator: %w", err)
	}

	idem, err := m.idempotency(ctx)
	if err != nil {
		return err
	}

	// Build the server first: the deprecated methods are read from the registered
	// services, and the chain below refers to them.
	deprecated := map[string]bool{}

	// Metrics and logging are outermost, so they see a recovered panic and a hidden
	// internal error as the Internal status the client gets. The rate limit comes before
	// project interceptors, so a flood never reaches token validation; idempotency comes
	// last, so a rejected call is not remembered.
	platformUnary := []grpc.UnaryServerInterceptor{m.metrics.unary, logUnary(m.log), recoverUnary(m.log, m.metrics), errorsUnary(m.log)}
	if m.cfg.RateLimit.RPS > 0 {
		if m.limiter, err = newRateLimiter(m.cfg.RateLimit, m.registry.public, m.app.Metrics()); err != nil {
			return fmt.Errorf("api: rate limit: %w", err)
		}
		platformUnary = append(platformUnary, m.limiter.unary)
	}
	platformUnary = append(platformUnary, deprecatedUnary(deprecated, m.log, m.deprecated))
	tail := []grpc.UnaryServerInterceptor{validateUnary(validator)}
	if idem != nil {
		tail = append(tail, idem.unary)
	}
	unary := slices.Concat(platformUnary, m.registry.unary, m.registry.authUnary, tail)
	stream := slices.Concat(
		[]grpc.StreamServerInterceptor{m.metrics.stream, logStream(m.log), recoverStream(m.log, m.metrics)},
		m.registry.stream,
		m.registry.authStream,
	)

	m.grpcSrv = grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
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
	m.recordMethods()
	// Every method shows up in the metrics from the start, with zero counts, so a rate is
	// defined before the first call.
	m.metrics.server.InitializeMetrics(m.grpcSrv)
	for method := range deprecatedMethods(m.grpcSrv) {
		deprecated[method] = true
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
		// The gateway's call carries the trace of the HTTP request on to the gRPC handler.
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
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
	gateway := newGateway(m.gatewayErrors())
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

// idempotency builds the idempotency check: the given store, or the database of the
// postgres module. Without either, keys are ignored, which is refused when a method
// requires one.
func (m *Module) idempotency(ctx context.Context) (*idempotency, error) {
	store := m.idemStore
	if store == nil {
		if pool, ok := platform.Lookup[*pgxpool.Pool](m.app); ok {
			pg, err := newPGIdempotencyStore(ctx, pool)
			if err != nil {
				return nil, err
			}
			store = pg
		}
	}
	if store == nil {
		if len(m.registry.idemRequired) > 0 {
			return nil, errors.New("api: idempotency needs the postgres module")
		}
		m.log.Info("idempotency is off: enable the postgres module to remember Idempotency-Key calls")
		return nil, nil
	}

	required := map[string]bool{}
	for _, method := range m.registry.idemRequired {
		required[method] = true
	}
	m.cleanup = make(chan struct{})
	done := m.cleanup // the goroutine keeps its own reference: Stop clears the field
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case now := <-ticker.C:
				if n, err := store.DeleteExpired(context.Background(), now); err != nil {
					m.log.Error("idempotency cleanup failed", "err", err)
				} else if n > 0 {
					m.log.Info("expired idempotency records removed", "count", n)
				}
			}
		}
	}()
	return &idempotency{
		store: store, required: required, user: m.registry.idemUser,
		retention: m.cfg.IdempotencyRetention, lock: m.cfg.IdempotencyLock,
		log: m.log, now: time.Now,
	}, nil
}

func (m *Module) recordMethods() {
	var methods []string
	for service, info := range m.grpcSrv.GetServiceInfo() {
		for _, method := range info.Methods {
			methods = append(methods, "/"+service+"/"+method.Name)
		}
	}
	slices.Sort(methods)
	m.registry.mu.Lock()
	m.registry.methods = methods
	m.registry.mu.Unlock()
}

func (m *Module) httpHandler(gateway *runtime.ServeMux) http.Handler {
	mux := http.NewServeMux()
	for _, r := range m.registry.routes {
		mux.Handle(r.pattern, withRoute(r.pattern, refuseTooLarge(r.handler)))
	}
	if m.cfg.Docs && m.openapi != nil {
		if spec, err := fs.ReadFile(m.openapi, "openapi.yaml"); err == nil {
			mux.Handle("GET /openapi.yaml", withRoute("/openapi.yaml", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/yaml")
				_, _ = w.Write(spec)
			})))
			mux.Handle("GET /docs", withRoute("/docs", http.HandlerFunc(docsPage)))
		}
	}
	mux.Handle("/", m.gatewayTooLarge(gateway))

	// From the inside out: project middleware and the body limit around the routes,
	// recovery inside the metrics and the access log so a panic is counted as a 500,
	// then CORS, security headers, and the request id and client address outermost.
	var h http.Handler = mux
	for i := len(m.registry.middleware) - 1; i >= 0; i-- {
		h = m.registry.middleware[i](h)
	}
	h = limitBody(int64(m.cfg.MaxRecvSize), h)
	h = recoverHTTP(m.log, m.metrics, h)
	h = m.http.observe(m.log, m.cfg.AccessLog, m.cfg.LogBodies, h)
	if len(m.cfg.CORSOrigins) > 0 {
		h = cors(m.cfg.CORSOrigins, h)
	}
	if m.cfg.SecurityHeaders {
		h = securityHeaders(h)
	}
	return otelhttp.NewHandler(identify(h), "http.gateway")
}

// newGateway configures the REST side: snake_case JSON as in the proto files, every
// field present in responses, unknown request fields ignored so old clients keep
// working, and request headers forwarded to the gRPC handlers as metadata.
func newGateway(errors runtime.ErrorHandlerFunc) *runtime.ServeMux {
	json := &runtime.JSONPb{
		MarshalOptions:   protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true},
		UnmarshalOptions: protojson.UnmarshalOptions{DiscardUnknown: true},
	}
	return runtime.NewServeMux(
		runtime.WithMarshalerOption(runtime.MIMEWildcard, json),
		runtime.WithMarshalerOption("multipart/form-data", newMultipartMarshaler(json)),
		runtime.WithIncomingHeaderMatcher(forwardHeader),
		runtime.WithErrorHandler(errors),
		runtime.WithRoutingErrorHandler(routingError),
		runtime.WithMetadata(annotateRoute),
	)
}

// hopByHop are headers that describe the HTTP connection, not the call.
var hopByHop = []string{
	"connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer",
	"transfer-encoding", "upgrade", "content-length", "content-type", "host", "accept-encoding",
}

func forwardHeader(key string) (string, bool) {
	lower := strings.ToLower(key)
	switch {
	case slices.Contains(hopByHop, lower):
		return "", false
	case lower == "user-agent":
		// gRPC sets its own user-agent on the gateway's connection; the client's travels
		// under the key grpc-gateway uses for it.
		return "grpcgateway-user-agent", true
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
	if m.skipped {
		return nil
	}
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
	if m.limiter != nil {
		m.limiter.stop()
	}
	if m.cleanup != nil {
		close(m.cleanup)
		m.cleanup = nil
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
