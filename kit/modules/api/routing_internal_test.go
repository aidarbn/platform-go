package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
)

// Routing errors carry taply's texts, the capital of grpc-gateway's "Unexpected" included.
func TestRoutingErrorTexts(t *testing.T) {
	mux := runtime.NewServeMux()
	for status, want := range map[int]string{
		http.StatusBadRequest:          `"Bad Request"`,
		http.StatusNotFound:            `"GET /v1/x: route not found"`,
		http.StatusMethodNotAllowed:    `"GET /v1/x: method not allowed"`,
		http.StatusInternalServerError: `"Unexpected routing error"`,
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/v1/x", nil)
		routingError(context.Background(), mux, &runtime.JSONPb{}, w, r, status)
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("%d: %s, want %s", status, w.Body.String(), want)
		}
	}
}
