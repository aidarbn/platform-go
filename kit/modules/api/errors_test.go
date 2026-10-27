package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	testv1 "github.com/aidarbn/platform-go/kit/internal/testapi/platformtest/v1"
	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/platform"
)

// A failure of an external provider is a 502 over REST, not the 503 a proxy takes for a
// dead replica; a provider timeout stays a 504 and a plain Unavailable a 503.
func TestExternalFailuresOverREST(t *testing.T) {
	s := start(t, api.Config{})
	for kind, want := range map[string]int{
		"external":            http.StatusBadGateway,
		"external_own":        http.StatusBadGateway,
		"external_timeout":    http.StatusGatewayTimeout,
		"unavailable":         http.StatusServiceUnavailable,
		"external_wrong_code": http.StatusInternalServerError,
	} {
		code, body, _ := s.do("GET", "/v1/fail/"+kind, "", nil)
		if code != want {
			t.Errorf("%s: %d, want %d: %s", kind, code, want, body)
		}
		if strings.HasPrefix(kind, "external") && kind != "external_wrong_code" && (!strings.Contains(body, "ErrorInfo") || !strings.Contains(body, `"provider"`)) {
			t.Errorf("%s: the body lost the ErrorInfo: %s", kind, body)
		}
	}
	if _, body, _ := s.do("GET", "/v1/fail/external", "", nil); !strings.Contains(body, "kaspi: payment service unavailable") || !strings.Contains(body, `"code":14`) {
		t.Errorf("body: %s", body)
	}
}

// A gRPC client gets Unavailable with the ErrorInfo.
func TestExternalFailureOverGRPC(t *testing.T) {
	s := start(t, api.Config{})
	conn, err := grpc.NewClient(s.module.GRPCAddr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = testv1.NewEchoServiceClient(conn).Fail(context.Background(), &testv1.FailRequest{Kind: "external"})
	st := status.Convert(err)
	if st.Code() != codes.Unavailable || !api.IsExternalUnavailable(err) {
		t.Fatalf("err = %v", err)
	}
	info, _ := st.Details()[0].(*errdetails.ErrorInfo)
	if info.GetDomain() != api.ErrorDomain || info.GetMetadata()["provider"] != "kaspi" {
		t.Errorf("info = %v", info)
	}
	if err := api.ExternalTimeout("x", "y"); status.Code(err) != codes.DeadlineExceeded || api.IsExternalUnavailable(err) {
		t.Errorf("timeout = %v", err)
	}
	if api.IsExternalUnavailable(errors.New("plain")) || api.IsExternalUnavailable(status.Error(codes.Unavailable, "down")) {
		t.Error("an error without the reason counts as external")
	}
}

// A project's own error writer gets the failure as the handler returned it and decides.
func TestExternalFailureWithOwnErrorHandler(t *testing.T) {
	var seen error
	extraWire = func(app *platform.App) {
		api.HTTPErrorHandler(app, func(ctx context.Context, mux *runtime.ServeMux, m runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
			seen = err
			w.WriteHeader(http.StatusTeapot)
		})
	}
	t.Cleanup(func() { extraWire = nil })
	s := start(t, api.Config{})
	if code, _, _ := s.do("GET", "/v1/fail/external", "", nil); code != http.StatusTeapot {
		t.Errorf("code = %d", code)
	}
	var withStatus *runtime.HTTPStatusError
	if errors.As(seen, &withStatus) || status.Code(seen) != codes.Unavailable || !api.IsExternalUnavailable(seen) {
		t.Errorf("the handler got %#v", seen)
	}
}

func TestExternalErrorDetails(t *testing.T) {
	info := func(err error) *errdetails.ErrorInfo {
		for _, d := range status.Convert(err).Details() {
			if i, ok := d.(*errdetails.ErrorInfo); ok {
				return i
			}
		}
		return nil
	}
	timeout := api.ExternalTimeout("kaspi", "payment service timeout")
	if i := info(timeout); status.Code(timeout) != codes.DeadlineExceeded || i.GetReason() != api.ReasonExternalTimeout || i.GetMetadata()["provider"] != "kaspi" {
		t.Errorf("timeout: %v %v", timeout, i)
	}
	// Without a provider the message stays as given.
	bare := api.ExternalUnavailable("", "down")
	if st := status.Convert(bare); st.Message() != "down" || len(info(bare).GetMetadata()) != 0 {
		t.Errorf("no provider: %q %v", st.Message(), info(bare))
	}
	// A wrapped status still counts.
	if !api.IsExternalUnavailable(fmt.Errorf("charge: %w", api.ExternalUnavailable("kaspi", "down"))) {
		t.Error("a wrapped failure is not recognised")
	}
	// The reason on another code is not a provider failure.
	if api.IsExternalUnavailable(status.ErrorProto(&spb.Status{Code: int32(codes.Internal)})) {
		t.Error("Internal counts")
	}
}

// The 502 rule leaves the 413 of a body over the limit alone.
func TestBodyTooLargeKeepsItsCode(t *testing.T) {
	s := start(t, api.Config{MaxRecvSize: 1024})
	if code, _, _ := s.do("POST", "/v1/echo", `{"message_text":"`+strings.Repeat("x", 4096)+`"}`, nil); code != http.StatusRequestEntityTooLarge {
		t.Errorf("code = %d", code)
	}
}
