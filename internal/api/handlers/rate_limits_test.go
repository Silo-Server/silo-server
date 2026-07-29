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
	values       map[string]string
	setCalls     int
	setManyCalls int
	atomicCalls  int
}

func newFakeRateLimitStore() *fakeRateLimitStore {
	return &fakeRateLimitStore{values: make(map[string]string)}
}

func (s *fakeRateLimitStore) Get(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s *fakeRateLimitStore) Set(_ context.Context, key, value string) error {
	s.setCalls++
	s.values[key] = value
	return nil
}

func (s *fakeRateLimitStore) SetMany(_ context.Context, values map[string]string) error {
	s.setManyCalls++
	for key, value := range values {
		s.values[key] = value
	}
	return nil
}

func (s *fakeRateLimitStore) GetAll(_ context.Context) (map[string]string, error) {
	out := make(map[string]string, len(s.values))
	for k, v := range s.values {
		out[k] = v
	}
	return out, nil
}

func (s *fakeRateLimitStore) UpdateAtomic(
	ctx context.Context,
	update func(current map[string]string) (map[string]string, error),
) error {
	s.atomicCalls++
	current, err := s.GetAll(ctx)
	if err != nil {
		return err
	}
	writes, err := update(current)
	if err != nil || len(writes) == 0 {
		return err
	}
	return s.SetMany(ctx, writes)
}

type postCommitOverwriteRateLimitStore struct {
	*fakeRateLimitStore
}

func (s *postCommitOverwriteRateLimitStore) UpdateAtomic(
	ctx context.Context,
	update func(current map[string]string) (map[string]string, error),
) error {
	s.atomicCalls++
	current, err := s.GetAll(ctx)
	if err != nil {
		return err
	}
	writes, err := update(current)
	if err != nil || len(writes) == 0 {
		return err
	}
	if err := s.SetMany(ctx, writes); err != nil {
		return err
	}

	// Model a later request committing before this handler performs its
	// post-commit reload and restart decision.
	s.values["ratelimit.enabled"] = "false"
	return nil
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
	tracker := NewServerRestartStatusTracker()
	h := NewRateLimitHandler(store, nil, nil, tracker, true)

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
	if store.setManyCalls != 1 {
		t.Fatalf("atomic writes = %d, want 1", store.setManyCalls)
	}
	if !tracker.Snapshot().RestartRequired {
		t.Fatal("enabling a stopped limiter did not update server restart status")
	}

	resp = putRateLimitConfig(t, h, `{"enabled":false,"backend":"redis"}`)
	if resp["restart_required"] != false {
		t.Errorf("saving disabled config without a running limiter: restart_required = %v, want false", resp["restart_required"])
	}
}

func TestRateLimitHandlerUsesLatestCommittedSettingsAfterAtomicWrite(t *testing.T) {
	baseStore := newFakeRateLimitStore()
	baseStore.values["ratelimit.enabled"] = "false"
	store := &postCommitOverwriteRateLimitStore{fakeRateLimitStore: baseStore}
	h := NewRateLimitHandler(store, nil, nil, NewServerRestartStatusTracker())

	resp := putRateLimitConfig(t, h, `{"enabled":true}`)

	if resp["restart_required"] != false {
		t.Fatalf("restart_required = %v, want false from latest committed disabled state", resp["restart_required"])
	}
	if got := store.values["ratelimit.enabled"]; got != "false" {
		t.Fatalf("ratelimit.enabled = %q, want later committed value false", got)
	}
}

func TestRateLimitHandlerWithRunningLimiter(t *testing.T) {
	store := newFakeRateLimitStore()
	mw := newRunningRateLimitMiddleware(t, store)
	h := NewRateLimitHandler(store, mw, nil, NewServerRestartStatusTracker(), true)

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

func TestRateLimitHandlerRejectsExplicitZeroWithoutWriting(t *testing.T) {
	store := newFakeRateLimitStore()
	h := NewRateLimitHandler(store, nil, nil, NewServerRestartStatusTracker())

	req := httptest.NewRequest(http.MethodPut, "/admin/rate-limits/config", strings.NewReader(
		`{"tiers":{"standard":{"requests_per_second":0}}}`,
	))
	rec := httptest.NewRecorder()
	h.HandleUpdateConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	if store.setManyCalls != 0 || store.setCalls != 0 {
		t.Fatalf("invalid request wrote settings: SetMany=%d Set=%d", store.setManyCalls, store.setCalls)
	}
}

func TestRateLimitHandlerRejectsOverflowingGlobalRateWithoutWriting(t *testing.T) {
	store := newFakeRateLimitStore()
	h := NewRateLimitHandler(store, nil, nil, NewServerRestartStatusTracker())

	req := httptest.NewRequest(http.MethodPut, "/admin/rate-limits/config", strings.NewReader(
		`{"global_requests_per_second":1e308}`,
	))
	rec := httptest.NewRecorder()
	h.HandleUpdateConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	if store.setManyCalls != 0 || store.setCalls != 0 {
		t.Fatalf("invalid request wrote settings: SetMany=%d Set=%d", store.setManyCalls, store.setCalls)
	}
}

func TestRateLimitHandlerRejectsOverflowingBurstWithoutWriting(t *testing.T) {
	store := newFakeRateLimitStore()
	h := NewRateLimitHandler(store, nil, nil, NewServerRestartStatusTracker())

	req := httptest.NewRequest(http.MethodPut, "/admin/rate-limits/config", strings.NewReader(
		`{"ip_burst":9223372036854775807}`,
	))
	rec := httptest.NewRecorder()
	h.HandleUpdateConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	if store.setManyCalls != 0 || store.setCalls != 0 {
		t.Fatalf("invalid request wrote settings: SetMany=%d Set=%d", store.setManyCalls, store.setCalls)
	}
}

func TestRateLimitHandlerRejectsRedisWithoutRedisConfiguration(t *testing.T) {
	store := newFakeRateLimitStore()
	h := NewRateLimitHandler(store, nil, nil, NewServerRestartStatusTracker())

	req := httptest.NewRequest(http.MethodPut, "/admin/rate-limits/config", strings.NewReader(
		`{"backend":"redis"}`,
	))
	rec := httptest.NewRecorder()
	h.HandleUpdateConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	if store.setManyCalls != 0 {
		t.Fatalf("invalid Redis selection wrote settings: SetMany=%d", store.setManyCalls)
	}
}

func TestRateLimitHandlerRejectsMalformedPersistedRedisURL(t *testing.T) {
	store := newFakeRateLimitStore()
	store.values["redis.url"] = "not-a-url"
	h := NewRateLimitHandler(store, nil, nil, NewServerRestartStatusTracker())

	req := httptest.NewRequest(http.MethodPut, "/admin/rate-limits/config", strings.NewReader(
		`{"backend":"redis"}`,
	))
	rec := httptest.NewRecorder()
	h.HandleUpdateConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	if store.setManyCalls != 0 {
		t.Fatalf("invalid Redis selection wrote settings: SetMany=%d", store.setManyCalls)
	}
}

func TestRateLimitHandlerAcceptsBootstrapRedisDespiteStalePersistedURL(t *testing.T) {
	store := newFakeRateLimitStore()
	store.values["redis.url"] = " redis://cache.example.invalid:6379 "
	h := NewRateLimitHandler(store, nil, nil, NewServerRestartStatusTracker(), true)

	req := httptest.NewRequest(http.MethodPut, "/admin/rate-limits/config", strings.NewReader(
		`{"backend":"redis"}`,
	))
	rec := httptest.NewRecorder()
	h.HandleUpdateConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if store.values["ratelimit.backend"] != "redis" {
		t.Fatalf("ratelimit.backend = %q, want redis", store.values["ratelimit.backend"])
	}
}

func TestRateLimitHandlerDoesNotTreatActiveRedisAsDurableConfiguration(t *testing.T) {
	store := newFakeRateLimitStore()
	perKey := ratelimit.NewMemoryLimiter()
	global := ratelimit.NewMemoryLimiter()
	t.Cleanup(func() {
		perKey.Close()
		global.Close()
	})
	// isMemory=false models a process currently using Redis. With no stored or
	// bootstrap transport, that active state cannot survive the next restart.
	mw := ratelimit.NewMiddleware(perKey, global, store, false)
	h := NewRateLimitHandler(store, mw, nil, NewServerRestartStatusTracker())

	req := httptest.NewRequest(http.MethodPut, "/admin/rate-limits/config", strings.NewReader(
		`{"backend":"redis"}`,
	))
	rec := httptest.NewRecorder()
	h.HandleUpdateConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	if store.setManyCalls != 0 {
		t.Fatalf("invalid Redis selection wrote settings: SetMany=%d", store.setManyCalls)
	}
}
