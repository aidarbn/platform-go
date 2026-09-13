package api_test

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
	"testing"

	testv1 "github.com/aidarbn/platform-go/kit/internal/testapi/platformtest/v1"
	"github.com/aidarbn/platform-go/kit/modules/api"
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

// CreateThing answers with what the gRPC handler sees of the HTTP request.
func (echo) CreateThing(ctx context.Context, req *testv1.CreateThingRequest) (*testv1.CreateThingResponse, error) {
	return &testv1.CreateThingResponse{Id: api.RequestID(ctx) + "|" + api.ClientIP(ctx)}, nil
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
	for _, want := range []string{
		`"title":"Menu"`, `"avatar_name":"logo.png"`, `"avatar_type":"image/png"`, `"avatar_size":7`,
		`"author":"aidar"`, `"images":["a.png:A","b.png:B",":","d.png:D"]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("response lacks %s:\n%s", want, got)
		}
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
	if code != http.StatusOK || !strings.Contains(body, `"id":"req-42|10.0.0.9"`) {
		t.Fatalf("code = %d\n%s", code, body)
	}
	if h.Get("X-Request-ID") != "req-42" {
		t.Errorf("response id = %q", h.Get("X-Request-ID"))
	}

	_, body, h = s.do("POST", "/v1/things", `{"name":"x"}`, nil)
	id := h.Get("X-Request-ID")
	if len(id) != 16 || !strings.Contains(body, `"id":"`+id+`|127.0.0.1"`) {
		t.Errorf("generated id %q, body %s", id, body)
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
		`api_http_requests_total{method="POST",route="/v1/echo",status="200"} 1`,
		`api_http_requests_total{method="POST",route="/v1/echo",status="400"} 1`,
		`api_http_requests_total{method="GET",route="unknown",status="404"} 1`,
		`api_http_requests_total{method="GET",route="GET /webhooks/ping",status="200"} 1`,
	} {
		if !strings.Contains(metrics, want) {
			t.Errorf("metrics lack %s", want)
		}
	}

	logs := s.logs.String()
	for _, want := range []string{
		`"msg":"http request"`, `"route":"/v1/echo"`, `"status":400`, `"req_body":"{\"message_text\":\"\"}"`,
		`"path":"/wp-login.php"`, `"route":"unknown"`,
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
