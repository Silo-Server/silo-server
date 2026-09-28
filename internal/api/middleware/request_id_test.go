package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	chimw "github.com/go-chi/chi/v5/middleware"
)

func TestRequestIDNeverAdoptsClientHeader(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = chimw.GetReqID(r.Context())
	}))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Request-Id", "client-chosen")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if seen == "" || seen == "client-chosen" || len(seen) != 24 {
		t.Fatalf("request id = %q", seen)
	}
	if got := rec.Header().Values("X-Request-Id"); len(got) != 0 {
		t.Fatalf("v1 chain must not echo a request-id header, got %v", got)
	}
	first := seen
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if seen == first {
		t.Fatal("request ids repeat")
	}
}
