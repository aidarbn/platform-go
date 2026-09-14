package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Headers the HTTP side sets for the gRPC side. They are always overwritten, so a client
// cannot supply them.
const (
	requestIDHeader = "X-Request-ID"
	clientIPHeader  = "X-Client-IP"
	maxBodyLogSize  = 4096
)

type httpContextKey int

const (
	requestIDKey httpContextKey = iota
	clientIPKey
	routeKey
)

// RequestID returns the id of the request: the X-Request-ID the client sent, or one
// generated for it. It works for HTTP handlers and for gRPC handlers behind the gateway.
func RequestID(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get(strings.ToLower(requestIDHeader)); len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// ClientIP returns the address of the client. Behind a proxy it is the last address of
// X-Forwarded-For — the one the proxy itself saw, which the client cannot forge — and
// the connection address otherwise. The gateway passes it on to gRPC handlers.
func ClientIP(ctx context.Context) string {
	if ip, ok := ctx.Value(clientIPKey).(string); ok {
		return ip
	}
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get(strings.ToLower(clientIPHeader)); len(v) > 0 && v[0] != "" {
			return v[0]
		}
	}
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		if host, _, err := net.SplitHostPort(p.Addr.String()); err == nil {
			return host
		}
		return p.Addr.String()
	}
	return ""
}

// userAgent returns the user agent of a call; the gateway forwards the one of the HTTP
// client under its own metadata key.
func userAgent(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	for _, key := range []string{"grpcgateway-user-agent", "user-agent"} {
		if v := md.Get(key); len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

func resolveClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// identify gives the request its id and client address, for the HTTP side through the
// context and for the gRPC side through headers the gateway forwards.
func identify(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get(requestIDHeader))
		if id == "" || len(id) > 128 {
			var b [8]byte
			_, _ = rand.Read(b[:])
			id = hex.EncodeToString(b[:])
		}
		ip := resolveClientIP(r)

		r.Header.Set(requestIDHeader, id)
		r.Header.Set(clientIPHeader, ip)
		w.Header().Set(requestIDHeader, id)

		ctx := context.WithValue(r.Context(), requestIDKey, id)
		ctx = context.WithValue(ctx, clientIPKey, ip)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// securityHeaders are taply's response headers for an API.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Cache-Control", "no-store")
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

// bodyLimitKey keeps the state of the body limit in the request context.
type bodyLimitKey struct{}

type bodyLimit struct {
	limit    int64
	exceeded atomic.Bool
}

// BodyTooLarge reports whether the request body is over API_MAX_RECV_SIZE: declared
// larger, or found larger while it was read. A project middleware that reads the body
// itself, such as a signature check, answers 413 when it is.
func BodyTooLarge(r *http.Request) bool {
	state, ok := r.Context().Value(bodyLimitKey{}).(*bodyLimit)
	return ok && state.exceeded.Load()
}

// limitBody caps the body. It does not answer itself, so the gateway can write the error
// in the format of the API: a body declared over the limit fails on the first read, and
// the routes and the gateway turn that into 413.
func limitBody(limit int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := &bodyLimit{limit: limit}
		r = r.WithContext(context.WithValue(r.Context(), bodyLimitKey{}, state))
		switch {
		case r.ContentLength > limit:
			state.exceeded.Store(true)
			r.Body = io.NopCloser(errReader{&http.MaxBytesError{Limit: limit}})
		case r.Body != nil:
			r.Body = &limitedBody{ReadCloser: http.MaxBytesReader(w, r.Body, limit), state: state}
		}
		next.ServeHTTP(w, r)
	})
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// limitedBody remembers that the reader ran over the limit.
type limitedBody struct {
	io.ReadCloser
	state *bodyLimit
}

func (b *limitedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		b.state.exceeded.Store(true)
	}
	return n, err
}

func tooLargeMessage(r *http.Request) string {
	var limit int64
	if state, ok := r.Context().Value(bodyLimitKey{}).(*bodyLimit); ok {
		limit = state.limit
	}
	return fmt.Sprintf("the request body is over %d bytes", limit)
}

func tooLargeError(r *http.Request) error {
	return &runtime.HTTPStatusError{
		HTTPStatus: http.StatusRequestEntityTooLarge,
		Err:        status.Error(codes.InvalidArgument, tooLargeMessage(r)),
	}
}

// refuseTooLarge answers a plain route with 413 before its handler runs.
func refuseTooLarge(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if BodyTooLarge(r) {
			http.Error(w, tooLargeMessage(r), http.StatusRequestEntityTooLarge)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// gatewayTooLarge refuses a gateway call whose body is declared over the limit, even when
// the method reads no body, through the error handler of the API.
func (m *Module) gatewayTooLarge(gateway *runtime.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if BodyTooLarge(r) {
			_, out := runtime.MarshalerForRequest(gateway, r)
			runtime.HTTPError(r.Context(), gateway, out, w, r, tooLargeError(r))
			return
		}
		gateway.ServeHTTP(w, r)
	})
}

// gatewayErrors is the error handler of the gateway: the project's, or the default one.
// A body that ran over the limit while the gateway decoded it becomes 413 first.
func (m *Module) gatewayErrors() runtime.ErrorHandlerFunc {
	handler := m.registry.errorHandler
	if handler == nil {
		handler = runtime.DefaultHTTPErrorHandler
	}
	return func(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
		var withStatus *runtime.HTTPStatusError
		if BodyTooLarge(r) && !errors.As(err, &withStatus) {
			err = tooLargeError(r)
		}
		handler(ctx, mux, marshaler, w, r, err)
	}
}

// cors lets browsers on the allowed origins call the REST API. Patterns follow taply:
// https://*.example.com for any subdomain, http://localhost:* for any port, * for any
// origin.
func cors(origins []string, next http.Handler) http.Handler {
	allowed := originMatcher(origins)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || !allowed(origin) {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Add("Vary", "Origin")
		h.Set("Access-Control-Allow-Credentials", "true")
		h.Set("Access-Control-Expose-Headers", requestIDHeader)
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, X-Request-ID, Idempotency-Key")
			h.Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func originMatcher(origins []string) func(string) bool {
	return func(origin string) bool {
		for _, o := range origins {
			switch {
			case o == "*" || o == origin:
				return true
			case strings.Contains(o, "://*."):
				i := strings.Index(o, "://*.")
				prefix, suffix := o[:i+3], o[i+4:]
				if strings.HasPrefix(origin, prefix) && strings.HasSuffix(origin, suffix) {
					sub := origin[len(prefix) : len(origin)-len(suffix)]
					if sub != "" && !strings.ContainsAny(sub, "/:") {
						return true
					}
				}
			case strings.HasSuffix(o, ":*"):
				prefix := strings.TrimSuffix(o, "*")
				if port, ok := strings.CutPrefix(origin, prefix); ok && port != "" {
					if _, err := strconv.Atoi(port); err == nil {
						return true
					}
				}
			}
		}
		return false
	}
}

// routeHolder carries the matched route template out of the gateway or the plain mux
// to the metrics and the access log, which run outside them.
type routeHolder struct{ route string }

func routeFrom(ctx context.Context) string {
	if h, ok := ctx.Value(routeKey).(*routeHolder); ok && h.route != "" {
		return h.route
	}
	return "unknown"
}

// annotateRoute is a gateway metadata annotator: by the time it runs the matched route
// template is known, and it is copied into the holder. It adds no metadata.
func annotateRoute(ctx context.Context, _ *http.Request) metadata.MD {
	if h, ok := ctx.Value(routeKey).(*routeHolder); ok {
		if pattern, ok := runtime.HTTPPathPattern(ctx); ok {
			h.route = pattern
		}
	}
	return nil
}

// withRoute names a plain route for the metrics.
func withRoute(pattern string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := r.Context().Value(routeKey).(*routeHolder); ok {
			h.route = pattern
		}
		next.ServeHTTP(w, r)
	})
}

// statusWriter records what was written, for the metrics and the access log.
type statusWriter struct {
	http.ResponseWriter
	status int
	size   int
	body   []byte // the start of the answer, kept for failed requests
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if room := maxBodyLogSize - len(w.body); room > 0 {
		w.body = append(w.body, p[:min(room, len(p))]...)
	}
	n, err := w.ResponseWriter.Write(p)
	w.size += n
	return n, err
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// httpMetrics are taply's gateway metrics — the names and labels the go-http dashboard
// and alerts of the monitoring agent read. path is the route template.
type httpMetrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	inFlight prometheus.Gauge
	size     *prometheus.HistogramVec
}

func newHTTPMetrics(reg *prometheus.Registry) (*httpMetrics, error) {
	m := &httpMetrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_gateway_requests_total", Help: "Total number of HTTP requests to the gateway",
		}, []string{"method", "path", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "http_gateway_request_duration_seconds", Help: "HTTP request duration in seconds",
			Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}, []string{"method", "path", "status"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "http_gateway_requests_in_flight", Help: "Number of HTTP requests currently being processed",
		}),
		size: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "http_gateway_response_size_bytes", Help: "HTTP response size in bytes",
			Buckets: []float64{100, 1000, 10000, 100000, 1000000, 10000000},
		}, []string{"method", "path"}),
	}
	for _, c := range []prometheus.Collector{m.requests, m.duration, m.inFlight, m.size} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("api http metrics: %w", err)
		}
	}
	return m, nil
}

// observe records metrics and the access log. The route is the template, never the
// concrete path: a path label per order id would grow without bound, and scanners
// hitting random paths all count as unknown.
func (m *httpMetrics) observe(log *slog.Logger, accessLog, logBodies bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		m.inFlight.Inc()
		defer m.inFlight.Dec()

		holder := &routeHolder{}
		r = r.WithContext(context.WithValue(r.Context(), routeKey, holder))

		var reqBody []byte
		if logBodies && r.Body != nil {
			r.Body = &captureBody{ReadCloser: r.Body, into: &reqBody}
		}
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}

		route := routeFrom(r.Context())
		took := time.Since(start)
		status := strconv.Itoa(sw.status)
		m.requests.WithLabelValues(r.Method, route, status).Inc()
		m.duration.WithLabelValues(r.Method, route, status).Observe(took.Seconds())
		m.size.WithLabelValues(r.Method, route).Observe(float64(sw.size))

		if !accessLog {
			return
		}
		attrs := []any{
			"method", r.Method, "path", r.URL.Path, "route", route, "status", sw.status,
			"duration", took, "ip", ClientIP(r.Context()), "request_id", RequestID(r.Context()),
			"user_agent", r.UserAgent(), "request_size", r.ContentLength, "response_size", sw.size,
		}
		if r.URL.RawQuery != "" {
			attrs = append(attrs, "query", r.URL.RawQuery)
		}
		if logBodies && sw.status >= 400 {
			if len(reqBody) > 0 {
				attrs = append(attrs, "req_body", bodyForLog(r.Header.Get("Content-Type"), reqBody, r.ContentLength))
			}
			if len(sw.body) > 0 {
				attrs = append(attrs, "res_body", bodyForLog(sw.Header().Get("Content-Type"), sw.body, int64(sw.size)))
			}
		}
		switch {
		case sw.status >= 500:
			log.ErrorContext(r.Context(), "http request", attrs...)
		case sw.status >= 400:
			log.WarnContext(r.Context(), "http request", attrs...)
		default:
			log.InfoContext(r.Context(), "http request", attrs...)
		}
	})
}

// captureBody keeps the start of the request body for the log of a failed request.
type captureBody struct {
	io.ReadCloser
	into *[]byte
}

func (c *captureBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if room := maxBodyLogSize - len(*c.into); room > 0 && n > 0 {
		*c.into = append(*c.into, p[:min(room, n)]...)
	}
	return n, err
}

// bodyForLog puts text into the log as is and replaces anything else with a summary: a
// rejected image upload dumped byte by byte breaks the log pipeline and puts customer
// files into the logs. Taken from taply.
func bodyForLog(contentType string, body []byte, size int64) string {
	if size < 0 {
		size = int64(len(body))
	}
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil && strings.HasPrefix(mediaType, "multipart/") {
		return fmt.Sprintf("[%s, %d bytes]", mediaType, size)
	}
	if !utf8.Valid(body) {
		return fmt.Sprintf("[binary, %d bytes]", size)
	}
	for _, r := range string(body) {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			return fmt.Sprintf("[binary, %d bytes]", size)
		}
	}
	return string(body)
}

// routingError names the method and the path of a request no route matched.
func routingError(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler, w http.ResponseWriter, r *http.Request, httpStatus int) {
	err := status.Error(codes.Internal, "unexpected routing error")
	switch httpStatus {
	case http.StatusBadRequest:
		err = status.Error(codes.InvalidArgument, http.StatusText(httpStatus))
	case http.StatusMethodNotAllowed:
		err = status.Errorf(codes.Unimplemented, "%s %s: method not allowed", r.Method, r.URL.Path)
	case http.StatusNotFound:
		err = status.Errorf(codes.NotFound, "%s %s: route not found", r.Method, r.URL.Path)
	}
	runtime.HTTPError(ctx, mux, marshaler, w, r, err)
}

// docsPage renders the OpenAPI description with Scalar. The page loads the renderer
// from a CDN; the description itself is served by the service at /openapi.yaml.
func docsPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>API</title>
</head>
<body>
<script id="api-reference" data-url="/openapi.yaml"></script>
<script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
</body>
</html>
`))
}
