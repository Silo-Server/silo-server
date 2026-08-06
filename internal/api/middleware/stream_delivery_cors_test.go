package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStreamDeliveryCORSWrapsErrorResponses(t *testing.T) {
	handler := StreamDeliveryCORS(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeUnauthorized(w, "missing credentials")
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/stream/session", nil))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, "*")
	}
	if got := recorder.Header().Get("Access-Control-Allow-Headers"); got != "Range" {
		t.Fatalf("Access-Control-Allow-Headers = %q, want %q", got, "Range")
	}
	if got := recorder.Header().Get("Access-Control-Expose-Headers"); got != "Content-Length, Content-Range" {
		t.Fatalf("Access-Control-Expose-Headers = %q, want %q", got, "Content-Length, Content-Range")
	}
}
