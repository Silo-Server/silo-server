package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
)

type serializedSettingsStore struct {
	mu sync.Mutex

	values map[string]string

	atomicCalls int
	active      int
	maxActive   int
	directSets  int
	directBatch int
}

type nonAtomicSettingsStore struct {
	values map[string]string
}

func (s *nonAtomicSettingsStore) Get(_ context.Context, key string) (string, error) {
	return s.values[key], nil
}

func (s *nonAtomicSettingsStore) GetAll(context.Context) (map[string]string, error) {
	return cloneSettings(s.values), nil
}

func (s *nonAtomicSettingsStore) Set(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

func newSerializedSettingsStore(values map[string]string) *serializedSettingsStore {
	return &serializedSettingsStore{values: values}
}

func (s *serializedSettingsStore) Get(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[key], nil
}

func (s *serializedSettingsStore) GetAll(context.Context) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneSettings(s.values), nil
}

func (s *serializedSettingsStore) Set(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.directSets++
	s.values[key] = value
	return nil
}

func (s *serializedSettingsStore) SetMany(_ context.Context, values map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.directBatch++
	for key, value := range values {
		s.values[key] = value
	}
	return nil
}

func (s *serializedSettingsStore) UpdateAtomic(
	_ context.Context,
	update func(current map[string]string) (map[string]string, error),
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.atomicCalls++
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	defer func() { s.active-- }()

	// Widen the race window: a handler that reads outside this capability would
	// allow both prospective snapshots to validate against the same state.
	time.Sleep(10 * time.Millisecond)
	writes, err := update(cloneSettings(s.values))
	if err != nil {
		return err
	}
	for key, value := range writes {
		s.values[key] = value
	}
	return nil
}

func cloneSettings(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func TestAdminSettingsAtomicUpdateSerializesCrossFieldValidation(t *testing.T) {
	store := newSerializedSettingsStore(map[string]string{
		"auth.access_token_expiry":  "8h",
		"auth.refresh_token_expiry": "30d",
	})
	handler := &AdminHandler{SettingsRepo: store}
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)

	run := func(body string) {
		<-start
		req := httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(body))
		rec := httptest.NewRecorder()
		handler.HandleUpdateSettings(rec, req)
		responses <- rec
	}
	go run(`{"values":{"auth.access_token_expiry":"48h"}}`)
	go run(`{"values":{"auth.refresh_token_expiry":"24h"}}`)
	close(start)

	first := <-responses
	second := <-responses
	okCount := 0
	badRequestCount := 0
	for _, response := range []*httptest.ResponseRecorder{first, second} {
		switch response.Code {
		case http.StatusOK:
			okCount++
		case http.StatusBadRequest:
			badRequestCount++
		default:
			t.Fatalf("unexpected status = %d body=%s", response.Code, response.Body.String())
		}
	}
	if okCount != 1 || badRequestCount != 1 {
		t.Fatalf("statuses = [%d, %d], want one 200 and one 400", first.Code, second.Code)
	}

	current, err := store.GetAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := validateProspectiveAdminSettings(current, false); err != nil {
		t.Fatalf("serialized final settings are invalid: %v; values=%#v", err, current)
	}
	if store.atomicCalls != 2 || store.maxActive != 1 {
		t.Fatalf("atomic calls=%d max active=%d, want 2 and 1", store.atomicCalls, store.maxActive)
	}
}

func TestAdminLegacySingleUpdateUsesAtomicSettingsBoundary(t *testing.T) {
	store := newSerializedSettingsStore(map[string]string{})
	handler := &AdminHandler{SettingsRepo: store}
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)

	go func() {
		<-start
		req := httptest.NewRequest(
			http.MethodPut,
			"/admin/settings",
			strings.NewReader(`{"values":{"branding.server_name":"Casa"}}`),
		)
		rec := httptest.NewRecorder()
		handler.HandleUpdateSettings(rec, req)
		responses <- rec
	}()
	go func() {
		<-start
		req := httptest.NewRequest(
			http.MethodPut,
			"/admin/settings/server.log_level",
			strings.NewReader(`{"value":"debug"}`),
		)
		req = withChiParam(req, "key", "server.log_level")
		rec := httptest.NewRecorder()
		handler.HandleUpdateSetting(rec, req)
		responses <- rec
	}()
	close(start)

	for range 2 {
		response := <-responses
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
		}
	}
	if store.atomicCalls != 2 || store.maxActive != 1 {
		t.Fatalf("atomic calls=%d max active=%d, want 2 and 1", store.atomicCalls, store.maxActive)
	}
	if store.directSets != 0 || store.directBatch != 0 {
		t.Fatalf("direct writes: Set=%d SetMany=%d, want zero", store.directSets, store.directBatch)
	}
}

func TestRateLimitAndAdminRedisUpdatesShareAtomicBoundary(t *testing.T) {
	store := newSerializedSettingsStore(map[string]string{
		"ratelimit.backend": "memory",
		"redis.url":         "redis://cache.example.invalid:6379",
	})
	adminHandler := &AdminHandler{SettingsRepo: store}
	rateLimitHandler := NewRateLimitHandler(store, nil, nil, NewServerRestartStatusTracker())
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)

	go func() {
		<-start
		req := httptest.NewRequest(
			http.MethodPut,
			"/admin/settings",
			strings.NewReader(`{"values":{"redis.url":""}}`),
		)
		rec := httptest.NewRecorder()
		adminHandler.HandleUpdateSettings(rec, req)
		responses <- rec
	}()
	go func() {
		<-start
		req := httptest.NewRequest(
			http.MethodPut,
			"/admin/rate-limits/config",
			strings.NewReader(`{"backend":"redis"}`),
		)
		rec := httptest.NewRecorder()
		rateLimitHandler.HandleUpdateConfig(rec, req)
		responses <- rec
	}()
	close(start)

	first := <-responses
	second := <-responses
	okCount := 0
	badRequestCount := 0
	for _, response := range []*httptest.ResponseRecorder{first, second} {
		switch response.Code {
		case http.StatusOK:
			okCount++
		case http.StatusBadRequest:
			badRequestCount++
		default:
			t.Fatalf("unexpected status = %d body=%s", response.Code, response.Body.String())
		}
	}
	if okCount != 1 || badRequestCount != 1 {
		t.Fatalf("statuses = [%d, %d], want one 200 and one 400", first.Code, second.Code)
	}

	current, err := store.GetAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateRedisRateLimitTransport(current, false); err != nil {
		t.Fatalf("serialized final Redis transport is invalid: %v; values=%#v", err, current)
	}
}

func TestAdminSettingsWritesFailClosedWithoutAtomicCapability(t *testing.T) {
	store := &nonAtomicSettingsStore{values: map[string]string{}}

	adminHandler := &AdminHandler{SettingsRepo: store}
	adminReq := httptest.NewRequest(
		http.MethodPut,
		"/admin/settings",
		strings.NewReader(`{"values":{"branding.server_name":"Casa"}}`),
	)
	adminRec := httptest.NewRecorder()
	adminHandler.HandleUpdateSettings(adminRec, adminReq)
	if adminRec.Code != http.StatusInternalServerError {
		t.Fatalf("Admin status = %d, want 500; body=%s", adminRec.Code, adminRec.Body.String())
	}

	rateLimitHandler := NewRateLimitHandler(store, nil, nil, NewServerRestartStatusTracker())
	rateReq := httptest.NewRequest(
		http.MethodPut,
		"/admin/rate-limits/config",
		strings.NewReader(`{"enabled":false}`),
	)
	rateRec := httptest.NewRecorder()
	rateLimitHandler.HandleUpdateConfig(rateRec, rateReq)
	if rateRec.Code != http.StatusInternalServerError {
		t.Fatalf("rate-limit status = %d, want 500; body=%s", rateRec.Code, rateRec.Body.String())
	}
	if len(store.values) != 0 {
		t.Fatalf("non-atomic store was mutated: %#v", store.values)
	}
}
