package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Once shutdown begins, readiness fails while the dependencies are fine, so a
// load balancer routes away before the listener closes. The body keeps the
// frozen shape of every non-ok answer: status plus each dependency boolean.
func TestReadinessFailsWhileShuttingDown(t *testing.T) {
	shuttingDown := false
	h := NewReadyHandler(pingerFunc(func(context.Context) error { return nil }), nil)
	h.SetShuttingDown(func() bool { return shuttingDown })

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/ready", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("before shutdown: %d %s", rec.Code, rec.Body.String())
	}

	shuttingDown = true
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/ready", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("shutting down: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "shutting_down" || body["postgres"] != true || body["s3"] != true || body["artwork"] != true {
		t.Fatalf("shutting down body = %v", body)
	}
}

// Without a shutdown signal wired, readiness behaves as before.
func TestReadinessWithoutShutdownSignal(t *testing.T) {
	h := NewReadyHandler(pingerFunc(func(context.Context) error { return nil }), nil)
	h.SetShuttingDown(nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/ready", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}
