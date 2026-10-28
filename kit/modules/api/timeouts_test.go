package api_test

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/platform"
)

func TestLoadHTTPTimeouts(t *testing.T) {
	l := confx.New("")
	cfg := api.Load(l)
	if err := l.Err(); err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPReadHeaderTimeout != 10*time.Second || cfg.HTTPReadTimeout != 0 ||
		cfg.HTTPWriteTimeout != 0 || cfg.HTTPIdleTimeout != 2*time.Minute {
		t.Fatalf("defaults: %v %v %v %v", cfg.HTTPReadHeaderTimeout, cfg.HTTPReadTimeout, cfg.HTTPWriteTimeout, cfg.HTTPIdleTimeout)
	}

	t.Setenv("API_HTTP_READ_HEADER_TIMEOUT", "5s")
	t.Setenv("API_HTTP_READ_TIMEOUT", "30s")
	t.Setenv("API_HTTP_WRITE_TIMEOUT", "60s")
	t.Setenv("API_HTTP_IDLE_TIMEOUT", "90s")
	l = confx.New("")
	cfg = api.Load(l)
	if err := l.Err(); err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPReadHeaderTimeout != 5*time.Second || cfg.HTTPReadTimeout != 30*time.Second ||
		cfg.HTTPWriteTimeout != time.Minute || cfg.HTTPIdleTimeout != 90*time.Second {
		t.Fatalf("values: %v %v %v %v", cfg.HTTPReadHeaderTimeout, cfg.HTTPReadTimeout, cfg.HTTPWriteTimeout, cfg.HTTPIdleTimeout)
	}

	for key, value := range map[string]string{
		"API_HTTP_READ_HEADER_TIMEOUT": "0", // never off: slowloris
		"API_HTTP_IDLE_TIMEOUT":        "0",
		"API_HTTP_READ_TIMEOUT":        "-1s",
		"API_HTTP_WRITE_TIMEOUT":       "-1s",
	} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			l := confx.New("")
			api.Load(l)
			if err := l.Err(); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// waitClosed reads the connection until the server closes it, and fails the test when
// it does not within the limit. It returns what the server wrote before closing.
func waitClosed(t *testing.T, conn net.Conn, limit time.Duration) string {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(limit))
	got, err := io.ReadAll(conn)
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("the server kept the connection open for %s", limit)
	}
	return string(got)
}

func dial(t *testing.T, s *service) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", s.module.HTTPAddr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// A client that sends its headers slower than API_HTTP_READ_HEADER_TIMEOUT allows is
// cut off: slowloris.
func TestHTTPReadHeaderTimeoutCutsSlowClient(t *testing.T) {
	s := start(t, api.Config{HTTPReadHeaderTimeout: 200 * time.Millisecond})
	conn := dial(t, s)
	begin := time.Now()
	if _, err := io.WriteString(conn, "GET /webhooks/ping HTTP/1.1\r\nHost: x\r\n"); err != nil {
		t.Fatal(err)
	}
	if got := waitClosed(t, conn, 3*time.Second); strings.Contains(got, "pong") {
		t.Fatalf("answered an unfinished request: %q", got)
	}
	if took := time.Since(begin); took < 150*time.Millisecond {
		t.Errorf("cut off after %s, before the timeout", took)
	}
}

// A request whose body arrives slower than API_HTTP_READ_TIMEOUT allows fails to read
// instead of holding the handler.
func TestHTTPReadTimeoutCutsSlowBody(t *testing.T) {
	bodyErr := make(chan error, 1)
	extraWire = func(app *platform.App) {
		api.HandleHTTP(app, "POST /upload", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := io.ReadAll(r.Body)
			bodyErr <- err
		}))
	}
	t.Cleanup(func() { extraWire = nil })
	s := start(t, api.Config{HTTPReadTimeout: 300 * time.Millisecond})
	conn := dial(t, s)
	if _, err := io.WriteString(conn, "POST /upload HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\n0123456789"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-bodyErr:
		if err == nil {
			t.Fatal("the body was read whole")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the slow body was not cut off")
	}
	waitClosed(t, conn, 3*time.Second)
}

// slowRoute answers after a pause, and streams its answer in parts when stream is set.
func slowRoute(pause time.Duration, parts int) func(app *platform.App) {
	return func(app *platform.App) {
		api.HandleHTTP(app, "GET /slow", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			rc := http.NewResponseController(w)
			for i := range parts {
				time.Sleep(pause)
				_, _ = fmt.Fprintf(w, "part %d\n", i)
				_ = rc.Flush()
			}
		}))
	}
}

// A call that answers later than API_HTTP_WRITE_TIMEOUT allows loses its answer.
func TestHTTPWriteTimeoutCutsLongHandler(t *testing.T) {
	extraWire = slowRoute(600*time.Millisecond, 1)
	t.Cleanup(func() { extraWire = nil })
	s := start(t, api.Config{HTTPWriteTimeout: 200 * time.Millisecond})
	resp, err := http.Get("http://" + s.module.HTTPAddr() + "/slow")
	if err == nil {
		body, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if rerr == nil && resp.StatusCode == http.StatusOK && strings.Contains(string(body), "part 0") {
			t.Fatalf("the answer arrived past the write timeout: %q", body)
		}
	}
}

// Without API_HTTP_WRITE_TIMEOUT, its default, a streamed answer that takes longer than
// the other timeouts of the server is delivered whole: the read timeout does not bound
// the answer.
func TestHTTPNoWriteTimeoutByDefaultKeepsStreams(t *testing.T) {
	extraWire = slowRoute(150*time.Millisecond, 5)
	t.Cleanup(func() { extraWire = nil })
	s := start(t, api.Config{HTTPReadTimeout: 200 * time.Millisecond})
	resp, err := http.Get("http://" + s.module.HTTPAddr() + "/slow")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || !strings.Contains(string(body), "part 4") {
		t.Fatalf("%q, %v", body, err)
	}
}

// A preflight on a keep-alive connection, after a call and after pauses longer than
// the read and write timeouts, is answered like on a fresh one: the server sets the
// deadlines for each request, not once for the connection. Both the CORS answer and
// the 204 of API_CORS_PREFLIGHT_NO_CONTENT keep the connection, also for an OPTIONS
// with a body the answer does not read. The connection is closed after the idle
// timeout.
func TestPreflightOnKeepAliveConnection(t *testing.T) {
	s := start(t, api.Config{
		CORSOrigins:            []string{"https://app.example.com"},
		CORSPreflightNoContent: true,
		HTTPReadTimeout:        300 * time.Millisecond,
		HTTPWriteTimeout:       300 * time.Millisecond,
		HTTPIdleTimeout:        3 * time.Second,
	})
	conn := dial(t, s)
	r := bufio.NewReader(conn)
	send := func(req string) *http.Response {
		t.Helper()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := io.WriteString(conn, req); err != nil {
			t.Fatalf("write: %v", err)
		}
		resp, err := http.ReadResponse(r, nil)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.Close {
			t.Fatalf("the server closes the connection after %s", resp.Status)
		}
		return resp
	}
	const preflight = "OPTIONS /v1/things HTTP/1.1\r\nHost: x\r\nOrigin: %s\r\nAccess-Control-Request-Method: POST\r\nAccess-Control-Request-Headers: content-type\r\n%s\r\n%s"

	if resp := send("GET /webhooks/ping HTTP/1.1\r\nHost: x\r\n\r\n"); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET: %s", resp.Status)
	}
	time.Sleep(500 * time.Millisecond) // past the read and write timeouts of the GET
	resp := send(fmt.Sprintf(preflight, "https://app.example.com", "", ""))
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != "https://app.example.com" {
		t.Fatalf("allowed preflight: %s %v", resp.Status, resp.Header)
	}
	time.Sleep(500 * time.Millisecond)
	resp = send(fmt.Sprintf(preflight, "https://evil.example.org", "Content-Length: 5\r\n", "hello"))
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("refused preflight: %s %v", resp.Status, resp.Header)
	}
	time.Sleep(500 * time.Millisecond)
	if resp := send("POST /v1/things HTTP/1.1\r\nHost: x\r\nOrigin: https://app.example.com\r\nContent-Type: application/json\r\nContent-Length: 12\r\n\r\n{\"name\":\"a\"}"); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST after the preflights: %s", resp.Status)
	}

	begin := time.Now()
	_ = conn.SetDeadline(time.Time{})
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if n, err := r.Read(make([]byte, 1)); n != 0 || err == nil {
		t.Fatalf("idle connection: %d bytes, %v", n, err)
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("the idle connection was not closed")
	}
	if took := time.Since(begin); took < 2*time.Second {
		t.Errorf("idle connection closed after %s, before the idle timeout", took)
	}
}
