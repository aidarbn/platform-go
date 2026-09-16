package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/netip"
	"net/textproto"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aidarbn/platform-go/kit/confx"
	testv1 "github.com/aidarbn/platform-go/kit/internal/testapi/platformtest/v1"
	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/platform"
)

// syncBuffer collects log output written from several goroutines.
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

func (echo) Upload(_ context.Context, req *testv1.UploadRequest) (*testv1.UploadResponse, error) {
	resp := &testv1.UploadResponse{
		Title:      req.GetTitle(),
		AvatarName: req.GetAvatar().GetFilename(),
		AvatarType: req.GetAvatar().GetContentType(),
		AvatarSize: int32(len(req.GetAvatar().GetContent())),
		Author:     req.GetMeta().GetAuthor(),
	}
	for _, img := range req.GetImages() {
		resp.Images = append(resp.Images, img.GetFilename()+":"+string(img.GetContent()))
	}
	return resp, nil
}

// thingCalls counts how often CreateThing really ran.
var thingCalls atomic.Int32

// CreateThing answers with what the gRPC handler sees of the HTTP request.
func (echo) CreateThing(ctx context.Context, req *testv1.CreateThingRequest) (*testv1.CreateThingResponse, error) {
	return &testv1.CreateThingResponse{Id: api.RequestID(ctx) + "|" + api.ClientIP(ctx), Calls: thingCalls.Add(1)}, nil
}

func (e echo) OldEcho(ctx context.Context, req *testv1.EchoRequest) (*testv1.EchoResponse, error) {
	return e.Echo(ctx, req)
}

func multipartBody(t *testing.T, build func(w *multipart.Writer)) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	build(w)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, w.FormDataContentType()
}

func filePart(t *testing.T, w *multipart.Writer, field, name, contentType, content string) {
	t.Helper()
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="`+field+`"; filename="`+name+`"`)
	h.Set("Content-Type", contentType)
	part, err := w.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte(content))
}

// Files come through the gateway the way taply takes them.
func TestMultipartUpload(t *testing.T) {
	s := start(t, api.Config{})

	body, contentType := multipartBody(t, func(w *multipart.Writer) {
		_ = w.WriteField("title", "Menu")
		filePart(t, w, "avatar", "logo.png", "image/png", "PNGDATA")
		filePart(t, w, "images", "a.png", "image/png", "A")
		filePart(t, w, "images", "b.png", "image/png", "B")
		filePart(t, w, "images[3]", "d.png", "image/png", "D") // index 2 is skipped
		_ = w.WriteField("meta", `{"author":"aidar"}`)
		_ = w.WriteField("unknown_part", "ignored")
	})
	code, got, _ := s.do("POST", "/v1/upload", body.String(), map[string]string{"Content-Type": contentType})
	if code != http.StatusOK {
		t.Fatalf("code = %d\n%s", code, got)
	}
	// protojson spaces its output at random on purpose, so the answer is decoded, not
	// compared as text.
	var resp struct {
		Title      string   `json:"title"`
		AvatarName string   `json:"avatar_name"`
		AvatarType string   `json:"avatar_type"`
		AvatarSize int      `json:"avatar_size"`
		Author     string   `json:"author"`
		Images     []string `json:"images"`
	}
	if err := json.Unmarshal([]byte(got), &resp); err != nil {
		t.Fatalf("decode: %v\n%s", err, got)
	}
	if resp.Title != "Menu" || resp.AvatarName != "logo.png" || resp.AvatarType != "image/png" || resp.AvatarSize != 7 || resp.Author != "aidar" {
		t.Errorf("response = %+v", resp)
	}
	if strings.Join(resp.Images, ",") != "a.png:A,b.png:B,:,d.png:D" {
		t.Errorf("images = %v", resp.Images)
	}

	// A huge index is refused instead of allocating a list of empty files.
	body, contentType = multipartBody(t, func(w *multipart.Writer) {
		filePart(t, w, "images[5000]", "x.png", "image/png", "X")
	})
	if code, got, _ := s.do("POST", "/v1/upload", body.String(), map[string]string{"Content-Type": contentType}); code != http.StatusBadRequest {
		t.Errorf("a huge index: %d %s", code, got)
	}
}

func TestBodyLimit(t *testing.T) {
	s := start(t, api.Config{MaxRecvSize: 1024})
	code, _, _ := s.do("POST", "/v1/echo", `{"message_text":"`+strings.Repeat("x", 4096)+`"}`, nil)
	if code != http.StatusRequestEntityTooLarge {
		t.Errorf("code = %d", code)
	}
}

// The request id and the client address reach the gRPC handler, and the address cannot
// be forged by the client.
func TestRequestIDAndClientIP(t *testing.T) {
	s := start(t, api.Config{})

	code, body, h := s.do("POST", "/v1/things", `{"name":"x"}`, map[string]string{
		"X-Request-ID":    "req-42",
		"X-Forwarded-For": "6.6.6.6, 10.0.0.9", // the last hop is the one the proxy saw
		"X-Client-IP":     "1.2.3.4",           // forged
	})
	if code != http.StatusOK || thingID(t, body) != "req-42|10.0.0.9" {
		t.Fatalf("code = %d\n%s", code, body)
	}
	if h.Get("X-Request-ID") != "req-42" {
		t.Errorf("response id = %q", h.Get("X-Request-ID"))
	}

	_, body, h = s.do("POST", "/v1/things", `{"name":"x"}`, nil)
	id := h.Get("X-Request-ID")
	if _, err := uuid.Parse(id); err != nil || id[14] != '7' || thingID(t, body) != id+"|127.0.0.1" {
		t.Errorf("generated id %q is not a uuidv7, body %s", id, body)
	}
}

// Only a trusted proxy may name the client: a request straight from elsewhere keeps its
// connection address whatever X-Forwarded-For says.
func TestUntrustedForwardedFor(t *testing.T) {
	s := start(t, api.Config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.9.9.0/24")}})
	code, body, _ := s.do("POST", "/v1/things", `{"name":"x"}`, map[string]string{"X-Forwarded-For": "10.0.0.9"})
	if code != http.StatusOK || !strings.HasSuffix(thingID(t, body), "|127.0.0.1") {
		t.Errorf("a forged address was believed: %d %s", code, body)
	}
}

func TestTrustedProxiesFromEnvironment(t *testing.T) {
	t.Setenv("API_TRUSTED_PROXIES", "10.1.0.0/16, 192.0.2.7")
	l := confx.New("")
	cfg := api.Load(l)
	if l.Err() != nil || len(cfg.TrustedProxies) != 2 || cfg.TrustedProxies[1].String() != "192.0.2.7/32" {
		t.Errorf("%v %v", cfg.TrustedProxies, l.Err())
	}
	t.Setenv("API_TRUSTED_PROXIES", "none")
	if cfg := api.Load(confx.New("")); cfg.TrustedProxies == nil || len(cfg.TrustedProxies) != 0 {
		t.Errorf("none: %v", cfg.TrustedProxies)
	}
	t.Setenv("API_TRUSTED_PROXIES", "proxy.local")
	l = confx.New("")
	api.Load(l)
	if l.Err() == nil {
		t.Error("a bad network is accepted")
	}
}

func TestSecurityHeadersAndCORSPatterns(t *testing.T) {
	s := start(t, api.Config{SecurityHeaders: true, CORSOrigins: []string{"https://*.example.com", "http://localhost:*"}})

	_, _, h := s.do("POST", "/v1/echo", `{"message_text":"x"}`, nil)
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" || h.Get("Cache-Control") != "no-store" {
		t.Errorf("security headers = %v", h)
	}

	for origin, allowed := range map[string]bool{
		"https://app.example.com":       true,
		"http://localhost:5173":         true,
		"https://example.com":           false,
		"http://app.example.com":        false,
		"https://evil.com/.example.com": false,
		"http://localhost:abc":          false,
	} {
		_, _, h := s.do("POST", "/v1/echo", `{"message_text":"x"}`, map[string]string{"Origin": origin})
		if got := h.Get("Access-Control-Allow-Origin") == origin; got != allowed {
			t.Errorf("%s: allowed = %v, want %v", origin, got, allowed)
		}
	}
}

func TestHTTPMetricsAndAccessLog(t *testing.T) {
	s := start(t, api.Config{AccessLog: true, LogBodies: true})

	s.do("POST", "/v1/echo", `{"message_text":"hi"}`, nil)
	s.do("POST", "/v1/echo", `{"message_text":""}`, nil) // rejected by validation
	s.do("GET", "/wp-login.php", "", nil)
	s.do("GET", "/webhooks/ping", "", nil)

	metrics := scrape(t, s.ops)
	for _, want := range []string{
		`http_gateway_requests_total{method="POST",path="/v1/echo",status="200"} 1`,
		`http_gateway_requests_total{method="POST",path="/v1/echo",status="400"} 1`,
		`http_gateway_requests_total{method="GET",path="unknown",status="404"} 1`,
		`http_gateway_requests_total{method="GET",path="GET /webhooks/ping",status="200"} 1`,
		`http_gateway_request_duration_seconds_count{method="POST",path="/v1/echo",status="200"} 1`,
	} {
		if !strings.Contains(metrics, want) {
			t.Errorf("metrics lack %s", want)
		}
	}

	logs := s.logs.String()
	if strings.Contains(logs, `"path":"/webhooks/ping?`) {
		t.Error("the access log keeps the raw path of a matched route")
	}
	for _, want := range []string{
		`"msg":"http request"`, `"route":"/v1/echo"`, `"status":400`, `"req_body":"{\"message_text\":\"\"}"`,
		`"path":"/wp-login.php"`, `"route":"unknown"`,
		// A matched route never puts the concrete path in the log: it may carry a token.
		`"path":"/v1/echo"`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %s", want)
		}
	}

	// A route that does not exist names the method and the path.
	if code, body, _ := s.do("GET", "/v1/nothing", "", nil); code != http.StatusNotFound || !strings.Contains(body, "GET /v1/nothing: route not found") {
		t.Errorf("unknown route: %d %s", code, body)
	}
}

func thingID(t *testing.T, body string) string {
	t.Helper()
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode: %v\n%s", err, body)
	}
	return resp.ID
}

// An API with its own error contract replaces the error body and the status codes for
// every error of the gateway, including a body over the limit.
func TestHTTPErrorHandler(t *testing.T) {
	var sawTooLarge atomic.Bool
	extraWire = func(app *platform.App) {
		api.HTTPErrorHandler(app, func(ctx context.Context, _ *runtime.ServeMux, _ runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
			code := http.StatusInternalServerError
			var withStatus *runtime.HTTPStatusError
			if errors.As(err, &withStatus) {
				code = withStatus.HTTPStatus
			} else if st, ok := status.FromError(err); ok && st.Code() == codes.InvalidArgument {
				code = http.StatusUnprocessableEntity
			} else if ok && st.Code() == codes.NotFound {
				code = http.StatusNotFound
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]any{"request_id": api.RequestID(ctx), "error": map[string]string{"message": status.Convert(err).Message()}})
		})
		// A middleware that reads the body itself learns that it is too large.
		api.UseHTTP(app, func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/echo" && r.Method == http.MethodPut {
					_, _ = io.ReadAll(r.Body)
					sawTooLarge.Store(api.BodyTooLarge(r))
					w.WriteHeader(http.StatusNoContent)
					return
				}
				next.ServeHTTP(w, r)
			})
		})
		api.HandleHTTP(app, "POST /webhooks/in", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		}))
	}
	t.Cleanup(func() { extraWire = nil })
	s := start(t, api.Config{MaxRecvSize: 1024})

	contract := func(body string) map[string]any {
		t.Helper()
		var v map[string]any
		if err := json.Unmarshal([]byte(body), &v); err != nil || v["request_id"] == "" || v["error"] == nil {
			t.Errorf("not the contract body: %s", body)
		}
		return v
	}

	code, body, _ := s.do("POST", "/v1/echo", `{"message_text":""}`, nil)
	if code != http.StatusUnprocessableEntity {
		t.Errorf("validation: %d %s", code, body)
	}
	contract(body)

	code, body, _ = s.do("GET", "/v1/nowhere", "", nil)
	if code != http.StatusNotFound {
		t.Errorf("routing: %d %s", code, body)
	}
	contract(body)

	huge := `{"message_text":"` + strings.Repeat("x", 4096) + `"}`
	code, body, _ = s.do("POST", "/v1/echo", huge, nil)
	if code != http.StatusRequestEntityTooLarge {
		t.Errorf("declared too large: %d %s", code, body)
	}
	contract(body)

	// A streamed body of unknown length runs over the limit while the gateway decodes it.
	req, _ := http.NewRequest("POST", "http://"+s.module.HTTPAddr()+"/v1/echo", io.MultiReader(strings.NewReader(huge)))
	req.ContentLength = -1
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("streamed too large: %d %s", resp.StatusCode, raw)
	}
	contract(string(raw))

	// Plain routes keep the plain 413; the middleware sees the flag.
	if code, _, _ := s.do("POST", "/webhooks/in", huge, nil); code != http.StatusRequestEntityTooLarge {
		t.Errorf("plain route: %d", code)
	}
	if code, _, _ := s.do("PUT", "/v1/echo", huge, nil); code != http.StatusNoContent || !sawTooLarge.Load() {
		t.Errorf("middleware: %d, too large = %v", code, sawTooLarge.Load())
	}
}
