package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubPinger struct{ err error }

func (s stubPinger) Ping(context.Context) error { return s.err }

type stubArtworkChecker struct{ err error }

func (s stubArtworkChecker) Check(context.Context) error { return s.err }

func readyResponse(t *testing.T, h *ReadyHandler) (int, readyStatus) {
	t.Helper()
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/ready", nil))
	var status readyStatus
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatalf("decoding readiness body %q: %v", recorder.Body.String(), err)
	}
	return recorder.Code, status
}

// A node that owns no artwork storage must still report ready.
func TestReadyWithoutArtworkStorage(t *testing.T) {
	handler := NewReadyHandler(stubPinger{}, nil)
	code, status := readyResponse(t, handler)
	if code != http.StatusOK || status.Status != "ok" {
		t.Fatalf("ready = %d %+v, want 200 ok", code, status)
	}
	if status.Artwork != nil {
		t.Fatalf("Artwork = %v, want omitted on a healthy response", *status.Artwork)
	}
}

func TestReadyWithHealthyArtworkStorage(t *testing.T) {
	handler := NewReadyHandler(stubPinger{}, nil)
	handler.SetArtworkStorage(stubArtworkChecker{})
	if code, status := readyResponse(t, handler); code != http.StatusOK || status.Status != "ok" {
		t.Fatalf("ready = %d %+v, want 200 ok", code, status)
	}
}

// An unwritable store leaves the API ready so resilient artwork fallback can
// continue, while exposing the degraded dependency to operators.
func TestReadyDegradesWhenArtworkStorageIsUnusable(t *testing.T) {
	handler := NewReadyHandler(stubPinger{}, nil)
	handler.SetArtworkStorage(stubArtworkChecker{err: errors.New("artwork root is not writable")})

	code, status := readyResponse(t, handler)
	if code != http.StatusOK || status.Status != "degraded" {
		t.Fatalf("ready = %d %+v, want 200 degraded", code, status)
	}
	if status.Artwork == nil || *status.Artwork {
		t.Fatalf("Artwork = %v, want false", status.Artwork)
	}
	if status.Postgres != nil {
		t.Fatalf("Postgres = %v, want omitted on a degradable artwork-only failure", status.Postgres)
	}
}

func TestReadyFailsWithoutPostgres(t *testing.T) {
	handler := NewReadyHandler(nil, nil)
	code, status := readyResponse(t, handler)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want 503", code)
	}
	if status.Postgres == nil || *status.Postgres {
		t.Fatalf("Postgres = %v, want false", status.Postgres)
	}
}
