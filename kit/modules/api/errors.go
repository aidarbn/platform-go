package api

import (
	"context"
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Reasons of the google.rpc.ErrorInfo the platform puts on a failure of an external
// provider, named as taply names them: a client and the gateway tell such a failure
// from the service's own by them.
const (
	// ReasonExternalUnavailable is a provider that answered with an error the client
	// cannot fix: a 5xx, a refusal of the service's own credentials, an answer without
	// a result. The REST gateway answers 502 for it.
	ReasonExternalUnavailable = "EXTERNAL_UNAVAILABLE"
	// ReasonExternalTimeout is a provider that did not answer in time; DeadlineExceeded
	// is a 504.
	ReasonExternalTimeout = "EXTERNAL_TIMEOUT"
)

// ErrorDomain is the domain of the ErrorInfo of ExternalUnavailable and ExternalTimeout.
const ErrorDomain = "platform-go"

// ExternalUnavailable is the error of a call that failed because an external provider
// failed: Unavailable with an ErrorInfo of ReasonExternalUnavailable that names the
// provider. Over REST it answers 502 Bad Gateway instead of the 503 of a plain
// Unavailable: a proxy such as nginx takes a 503 for a dead replica and stops sending to
// it, while the replica is fine and the provider is not. Idempotency lets a repeat run
// again, and the i18n module leaves the message as it is.
//
// The message is "provider: message" and goes to the client as it is: pass a short
// fixed text, never err.Error() or the body of the provider's answer, which can hold
// credentials or a whole HTML page — those belong in the log. An empty provider gives
// the message alone and an ErrorInfo without the provider.
//
//	return nil, api.ExternalUnavailable("kaspi", "payment service unavailable")
func ExternalUnavailable(provider, message string) error {
	return externalStatus(codes.Unavailable, ReasonExternalUnavailable, provider, message)
}

// ExternalTimeout is the error of a call whose external provider did not answer in
// time: DeadlineExceeded, a 504 over REST, with an ErrorInfo of ReasonExternalTimeout.
func ExternalTimeout(provider, message string) error {
	return externalStatus(codes.DeadlineExceeded, ReasonExternalTimeout, provider, message)
}

func externalStatus(code codes.Code, reason, provider, message string) error {
	text, meta := message, map[string]string{}
	if provider != "" {
		text, meta["provider"] = provider+": "+message, provider
	}
	st := status.New(code, text)
	detailed, err := st.WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: ErrorDomain, Metadata: meta})
	if err != nil {
		// WithDetails fails only for a status with code OK, which this never is; the
		// plain status is still the right code.
		return st.Err()
	}
	return detailed.Err()
}

// IsExternalUnavailable reports whether an error is Unavailable with an ErrorInfo of
// ReasonExternalUnavailable, whatever its domain: a status a project builds itself, as
// taply does, counts too; another code with that reason does not. For an
// api.HTTPErrorHandler that maps it like the platform.
func IsExternalUnavailable(err error) bool {
	return status.Code(err) == codes.Unavailable && hasReason(err, ReasonExternalUnavailable)
}

func hasReason(err error, reason string) bool {
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetReason() == reason {
			return true
		}
	}
	return false
}

// defaultHTTPErrors is the gateway's error writer with the platform's one rule on top:
// a failure of an external provider is a 502.
func defaultHTTPErrors(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
	if IsExternalUnavailable(err) {
		err = &runtime.HTTPStatusError{HTTPStatus: http.StatusBadGateway, Err: err}
	}
	runtime.DefaultHTTPErrorHandler(ctx, mux, marshaler, w, r, err)
}
