package api_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/aidarbn/platform-go/kit/confx"
	testv1 "github.com/aidarbn/platform-go/kit/internal/testapi/platformtest/v1"
	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/platform"
)

func TestLoadStopTimeouts(t *testing.T) {
	l := confx.New("")
	cfg := api.Load(l)
	if err := l.Err(); err != nil || cfg.HTTPStopTimeout != 0 || cfg.GRPCStopTimeout != 0 {
		t.Fatalf("defaults: %v %v, %v", cfg.HTTPStopTimeout, cfg.GRPCStopTimeout, err)
	}

	t.Setenv("API_HTTP_STOP_TIMEOUT", "5s")
	t.Setenv("API_GRPC_STOP_TIMEOUT", "1s")
	l = confx.New("")
	cfg = api.Load(l)
	if err := l.Err(); err != nil || cfg.HTTPStopTimeout != 5*time.Second || cfg.GRPCStopTimeout != time.Second {
		t.Fatalf("values: %v %v, %v", cfg.HTTPStopTimeout, cfg.GRPCStopTimeout, err)
	}

	t.Setenv("API_GRPC_STOP_TIMEOUT", "-1s")
	l = confx.New("")
	api.Load(l)
	if err := l.Err(); err == nil || !strings.Contains(err.Error(), "API_GRPC_STOP_TIMEOUT") {
		t.Fatalf("err = %v", err)
	}
}

func TestStopPhases(t *testing.T) {
	var got []string
	for _, p := range api.New(api.Config{}).StopPhases() {
		if p.Stop == nil {
			t.Fatalf("phase %s has no Stop", p.Name)
		}
		got = append(got, p.Name)
		switch p.Name {
		case "http":
			if p.Order != platform.PhaseHTTP {
				t.Errorf("http order %d", p.Order)
			}
		case "grpc":
			if p.Order != platform.PhaseGRPC {
				t.Errorf("grpc order %d", p.Order)
			}
		}
	}
	if strings.Join(got, ",") != "http,grpc" {
		t.Errorf("phases = %v", got)
	}
}

// stopAndWait stops the service and returns what Run returned and how long it took. It
// hands the start helper a nil result, so its cleanup does not report the error the
// test expects.
func stopAndWait(t *testing.T, s *service) (time.Duration, error) {
	t.Helper()
	begin := time.Now()
	s.stopRun()
	select {
	case err := <-s.errCh:
		s.errCh <- nil
		return time.Since(begin), err
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
		return 0, nil
	}
}

// API_HTTP_STOP_TIMEOUT cuts off a REST call that outlives it instead of letting it hold
// the shutdown until SHUTDOWN_TIMEOUT.
func TestHTTPStopTimeoutCutsSlowCall(t *testing.T) {
	s := start(t, api.Config{HTTPStopTimeout: 200 * time.Millisecond})

	done := make(chan int, 1)
	go func() {
		resp, err := http.Get("http://" + s.module.HTTPAddr() + "/v1/fail/slow")
		if err != nil {
			done <- 0
			return
		}
		resp.Body.Close()
		done <- resp.StatusCode
	}()
	time.Sleep(300 * time.Millisecond)

	took, err := stopAndWait(t, s)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Run = %v, want the HTTP budget reported", err)
	}
	if took > 1500*time.Millisecond {
		t.Errorf("shutdown took %s with a 200ms HTTP budget", took)
	}
	select {
	case code := <-done:
		if code == http.StatusOK {
			t.Error("the slow call finished although its budget was spent")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the slow call was not cut off")
	}
}

// API_GRPC_STOP_TIMEOUT cuts off a gRPC call that outlives it.
func TestGRPCStopTimeoutCutsSlowCall(t *testing.T) {
	s := start(t, api.Config{GRPCStopTimeout: 200 * time.Millisecond})
	conn, err := grpc.NewClient(s.module.GRPCAddr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	done := make(chan error, 1)
	go func() {
		_, err := testv1.NewEchoServiceClient(conn).Fail(context.Background(), &testv1.FailRequest{Kind: "slow"})
		done <- err
	}()
	time.Sleep(300 * time.Millisecond)

	took, err := stopAndWait(t, s)
	if err != nil {
		t.Errorf("Run = %v", err)
	}
	if took > 1500*time.Millisecond {
		t.Errorf("shutdown took %s with a 200ms gRPC budget", took)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Error("the slow call finished although its budget was spent")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the slow call was not cut off")
	}
}

// With phased shutdown the module stops through its phases, and a running call still
// finishes within the shutdown timeout.
func TestPhasedShutdownLetsCallsFinish(t *testing.T) {
	t.Setenv("SHUTDOWN_PHASES", "true")
	s := start(t, api.Config{})

	done := make(chan int, 1)
	go func() {
		resp, err := http.Get("http://" + s.module.HTTPAddr() + "/v1/fail/slow")
		if err != nil {
			done <- 0
			return
		}
		resp.Body.Close()
		done <- resp.StatusCode
	}()
	time.Sleep(300 * time.Millisecond)

	if _, err := stopAndWait(t, s); err != nil {
		t.Errorf("Run = %v", err)
	}
	if code := <-done; code != http.StatusOK {
		t.Errorf("the running call ended with %d", code)
	}
	if !strings.Contains(s.logs.String(), `"phase":"grpc"`) {
		t.Errorf("the gRPC phase is not in the log:\n%s", s.logs.String())
	}
}
