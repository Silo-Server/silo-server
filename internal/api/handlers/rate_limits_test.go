package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/ratelimit"
)

type fakeRateLimitStore struct {
	values map[string]string
}

func newFakeRateLimitStore() *fakeRateLimitStore {
	return &fakeRateLimitStore{values: make(map[string]string)}
}

func (s *fakeRateLimitStore) Get(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s *fakeRateLimitStore) Set(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

func (s *fakeRateLimitStore) GetAll(_ context.Context) (map[string]string, error) {
	out := make(map[string]string, len(s.values))
	for k, v := range s.values {
		out[k] = v
	}
	return out, nil
}

func newRunningRateLimitMiddleware(t *testing.T, store ratelimit.SettingsStore) *ratelimit.Middleware {
	t.Helper()
	perKey := ratelimit.NewMemoryLimiter()
	global := ratelimit.NewMemoryLimiter()
	t.Cleanup(func() {
		perKey.Close()
		global.Close()
	})
	mw := ratelimit.NewMiddleware(perKey, global, store, true)
	if err := mw.Init(context.Background()); err != nil {
		t.Fatalf("init middleware: %v", err)
	}
	return mw
}

func putRateLimitConfig(t *testing.T, h *RateLimitHandler, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/admin/rate-limits/config", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.HandleUpdateConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode PUT response: %v", err)
	}
	return resp
}

func getRateLimitConfig(t *testing.T, h *RateLimitHandler) rateLimitConfigResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin/rate-limits/config", nil)
	rec := httptest.NewRecorder()
	h.HandleGetConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp rateLimitConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	return resp
}

func TestRateLimitHandlerWithoutRunningLimiter(t *testing.T) {
	// The limiter is nil when rate limiting was disabled at boot. The config
	// endpoints must still work so an admin can re-enable it from the UI.
	store := newFakeRateLimitStore()
	store.values["ratelimit.enabled"] = "false"
	h := NewRateLimitHandler(store, nil, nil, NewServerRestartStatusTracker())

	got := getRateLimitConfig(t, h)
	if got.Enabled {
		t.Error("GET enabled = true, want false")
	}
	if got.Active {
		t.Error("GET active = true, want false when no limiter is running")
	}

	resp := putRateLimitConfig(t, h, `{"enabled":true,"backend":"redis"}`)
	if resp["restart_required"] != true {
		t.Errorf("enabling without a running limiter: restart_required = %v, want true", resp["restart_required"])
	}
	if store.values["ratelimit.enabled"] != "true" {
		t.Errorf("ratelimit.enabled = %q, want \"true\"", store.values["ratelimit.enabled"])
	}
	if store.values["ratelimit.backend"] != "redis" {
		t.Errorf("ratelimit.backend = %q, want \"redis\"", store.values["ratelimit.backend"])
	}

	resp = putRateLimitConfig(t, h, `{"enabled":false,"backend":"redis"}`)
	if resp["restart_required"] != false {
		t.Errorf("saving disabled config without a running limiter: restart_required = %v, want false", resp["restart_required"])
	}
}

func TestRateLimitHandlerWithRunningLimiter(t *testing.T) {
	store := newFakeRateLimitStore()
	mw := newRunningRateLimitMiddleware(t, store)
	h := NewRateLimitHandler(store, mw, nil, NewServerRestartStatusTracker())

	got := getRateLimitConfig(t, h)
	if !got.Active {
		t.Error("GET active = false, want true with a running limiter")
	}
	if got.ActiveBackend != "memory" {
		t.Errorf("GET active_backend = %q, want \"memory\"", got.ActiveBackend)
	}

	// Toggling enabled hot-reloads; no restart needed when the backend matches.
	resp := putRateLimitConfig(t, h, `{"enabled":false,"backend":"memory"}`)
	if resp["restart_required"] != false {
		t.Errorf("toggle on running limiter: restart_required = %v, want false", resp["restart_required"])
	}

	// Switching backend only takes effect at boot.
	resp = putRateLimitConfig(t, h, `{"enabled":true,"backend":"redis"}`)
	if resp["restart_required"] != true {
		t.Errorf("backend change: restart_required = %v, want true", resp["restart_required"])
	}
}
