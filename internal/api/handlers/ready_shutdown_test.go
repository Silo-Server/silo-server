package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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

// A shutting-down answer reports the last probe instead of waiting on a new
// one, so a hung dependency cannot hold back the 503. With no earlier probe it
// probes once.
func TestShuttingDownReadinessDoesNotWaitOnProbes(t *testing.T) {
	var pings atomic.Int32
	shuttingDown := false
	h := NewReadyHandler(pingerFunc(func(context.Context) error {
		pings.Add(1)
		if shuttingDown {
			return errors.New("probed while shutting down")
		}
		return nil
	}), nil)
	h.SetShuttingDown(func() bool { return shuttingDown })
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/ready", nil))

	shuttingDown = true
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/ready", nil))
	if got := pings.Load(); got != 1 {
		t.Fatalf("postgres pinged %d times, want only the probe before shutdown", got)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusServiceUnavailable || body["status"] != "shutting_down" || body["postgres"] != true {
		t.Fatalf("shutting down: %d %v", rec.Code, body)
	}

	fresh := NewReadyHandler(pingerFunc(func(context.Context) error { pings.Add(1); return nil }), nil)
	fresh.SetShuttingDown(func() bool { return true })
	rec = httptest.NewRecorder()
	fresh.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/ready", nil))
	if got := pings.Load(); got != 2 || rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("with no earlier probe: %d pings, status %d", got, rec.Code)
	}
}

// A shutdown that begins while readiness probes fails that answer too, so a
// probe in flight at the signal does not keep the server in rotation.
func TestShutdownDuringProbeFailsReadiness(t *testing.T) {
	var shuttingDown atomic.Bool
	h := NewReadyHandler(pingerFunc(func(context.Context) error {
		shuttingDown.Store(true)
		return nil
	}), nil)
	h.SetShuttingDown(shuttingDown.Load)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/ready", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d with a shutdown during the probe, want 503", rec.Code)
	}
}
