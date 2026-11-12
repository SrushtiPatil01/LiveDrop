package observability

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPMiddlewareCreatesAndPropagatesRequestID(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := HTTPMiddleware(logger, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if RequestID(r.Context()) == "" {
			t.Error("request ID missing from context")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("got status %d, want %d", response.Code, http.StatusNoContent)
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("X-Request-ID response header is missing")
	}
}
