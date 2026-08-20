package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

type stubWatchProviderService struct {
	providers []watchsync.ProviderSummary
	session   watchsync.DeviceAuthSession
	manual    watchsync.ManualSyncResult
	manualErr error
	runs      []watchsync.SyncRun
}

type capturingWatchProviderService struct {
	stubWatchProviderService
	apiKey string
	config watchsync.ConnectionConfigValues
}

func (s *capturingWatchProviderService) ConnectAPIKeyWithConfig(
	_ context.Context,
	_ int,
	_ string,
	_ string,
	apiKey string,
	config watchsync.ConnectionConfigValues,
) (watchsync.Connection, error) {
	s.apiKey = apiKey
	s.config = config
	return watchsync.Connection{}, nil
}

func (s stubWatchProviderService) ListProviders() []watchsync.ProviderSummary {
	return s.providers
}
func (s stubWatchProviderService) StartDeviceAuth(context.Context, int, string, string) (watchsync.DeviceAuthSession, error) {
	return s.session, nil
}
func (s stubWatchProviderService) PollDeviceAuth(context.Context, int, string, string, string) (watchsync.Connection, error) {
	return watchsync.Connection{}, nil
}
func (s stubWatchProviderService) ConnectAPIKeyWithConfig(context.Context, int, string, string, string, watchsync.ConnectionConfigValues) (watchsync.Connection, error) {
	return watchsync.Connection{}, nil
}
func (s stubWatchProviderService) GetConnectionStatus(context.Context, int, string, string) (watchsync.ConnectionStatus, error) {
	return watchsync.ConnectionStatus{}, nil
}
func (s stubWatchProviderService) UpdateConnection(context.Context, int, string, string, watchsync.ConnectionUpdate) (watchsync.ConnectionStatus, error) {
	return watchsync.ConnectionStatus{}, nil
}
func (s stubWatchProviderService) DeleteConnection(context.Context, int, string, string) error {
	return nil
}
func (s stubWatchProviderService) RequestManualSync(context.Context, int, string, string) (watchsync.ManualSyncResult, error) {
	return s.manual, s.manualErr
}
func (s stubWatchProviderService) ListSyncRuns(context.Context, int, string, string, int) ([]watchsync.SyncRun, error) {
	return s.runs, nil
}

func TestWatchProviderHandlerListsProviders(t *testing.T) {
	service := stubWatchProviderService{
		providers: []watchsync.ProviderSummary{
			{
				Key:         "trakt",
				DisplayName: "Trakt",
				Capabilities: watchsync.Capabilities{
					ImportWatched:    true,
					ImportProgress:   true,
					ExportWatched:    true,
					ScrobblePlayback: true,
				},
			},
		},
	}
	handler := NewWatchProviderHandler(service)

	req := httptest.NewRequest(http.MethodGet, "/watch-providers/", nil)
	rec := httptest.NewRecorder()

	handler.HandleListProviders(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q, want application/json", got)
	}

	var resp struct {
		Providers []watchsync.ProviderSummary `json:"providers"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(resp.Providers))
	}
	if !reflect.DeepEqual(resp.Providers[0], service.providers[0]) {
		t.Fatalf("provider = %#v, want %#v", resp.Providers[0], service.providers[0])
	}
}

func TestWatchProviderHandlerReturnsEmptyProviderList(t *testing.T) {
	handler := NewWatchProviderHandler(stubWatchProviderService{})

	req := httptest.NewRequest(http.MethodGet, "/watch-providers/", nil)
	rec := httptest.NewRecorder()

	handler.HandleListProviders(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Providers []watchsync.ProviderSummary `json:"providers"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Providers == nil {
		t.Fatal("providers = nil, want empty slice")
	}
	if len(resp.Providers) != 0 {
		t.Fatalf("providers length = %d, want 0", len(resp.Providers))
	}
}

func TestWatchProviderHandlerStartsDeviceAuthWithFrontendJSONShape(t *testing.T) {
	expiresAt := time.Date(2026, 5, 4, 15, 57, 8, 0, time.UTC)
	handler := NewWatchProviderHandler(stubWatchProviderService{
		session: watchsync.DeviceAuthSession{
			ID:              "auth-1",
			Provider:        "trakt",
			UserID:          7,
			ProfileID:       "profile-1",
			DeviceCode:      "device-code",
			UserCode:        "31B677B9",
			VerificationURL: "https://trakt.tv/activate",
			IntervalSeconds: 5,
			ExpiresAt:       expiresAt,
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/watch-providers/trakt/auth/device-code", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("provider", "trakt")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = middleware.SetClaims(ctx, &auth.Claims{UserID: 7})
	ctx = middleware.SetProfileID(ctx, "profile-1")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	handler.HandleStartDeviceAuth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, key := range []string{"ID", "UserCode", "VerificationURL", "IntervalSeconds", "ExpiresAt"} {
		if _, ok := resp[key]; ok {
			t.Fatalf("response contains Go-style key %q: %#v", key, resp)
		}
	}
	for _, key := range []string{"device_code", "user_id", "profile_id", "completed_at"} {
		if _, ok := resp[key]; ok {
			t.Fatalf("response exposes host-private field %q: %#v", key, resp)
		}
	}
	if resp["id"] != "auth-1" {
		t.Fatalf("id = %#v, want auth-1", resp["id"])
	}
	if resp["user_code"] != "31B677B9" {
		t.Fatalf("user_code = %#v, want 31B677B9", resp["user_code"])
	}
	if resp["verification_url"] != "https://trakt.tv/activate" {
		t.Fatalf("verification_url = %#v, want https://trakt.tv/activate", resp["verification_url"])
	}
}

func TestWatchProviderHandlerPassesConnectionConfigWithAPIKey(t *testing.T) {
	service := &capturingWatchProviderService{}
	handler := NewWatchProviderHandler(service)
	req := httptest.NewRequest(http.MethodPost, "/watch-providers/plugin:4:floppy/auth/api-key", strings.NewReader(`{
		"api_key":"token",
		"connection_config":{"floppy":{"base_url":"https://floppy.example.com"}}
	}`))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("provider", "plugin:4:floppy")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = middleware.SetClaims(ctx, &auth.Claims{UserID: 7})
	ctx = middleware.SetProfileID(ctx, "profile-1")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	handler.HandleConnectAPIKey(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if service.apiKey != "token" || service.config["floppy"]["base_url"] != "https://floppy.example.com" {
		t.Fatalf("api key = %q, config = %#v", service.apiKey, service.config)
	}
}

func TestWatchProviderHandlerManualSyncReturnsRun(t *testing.T) {
	startedAt := time.Date(2026, 5, 4, 16, 0, 0, 0, time.UTC)
	handler := NewWatchProviderHandler(stubWatchProviderService{
		manual: watchsync.ManualSyncResult{
			Run: watchsync.SyncRun{
				ID:           "run-1",
				ConnectionID: "conn-1",
				Provider:     "trakt",
				Trigger:      "manual",
				Status:       string(watchsync.SyncRunStatusRunning),
				StartedAt:    startedAt,
				CreatedAt:    startedAt,
			},
		},
	})

	req := authorizedWatchProviderRequest(http.MethodPost, "/watch-providers/trakt/sync")
	rec := httptest.NewRecorder()

	handler.HandleManualSync(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	var resp watchsync.ManualSyncResult
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Run.ID != "run-1" || resp.Run.Status != string(watchsync.SyncRunStatusRunning) {
		t.Fatalf("run = %+v", resp.Run)
	}
}

func TestWatchProviderHandlerManualSyncCooldown(t *testing.T) {
	handler := NewWatchProviderHandler(stubWatchProviderService{
		manualErr: watchsync.SyncCooldownError{RetryAfterSeconds: 2450},
	})

	req := authorizedWatchProviderRequest(http.MethodPost, "/watch-providers/trakt/sync")
	rec := httptest.NewRecorder()

	handler.HandleManualSync(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusTooManyRequests, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "2450" {
		t.Fatalf("Retry-After = %q, want 2450", got)
	}
	var resp struct {
		Error             string `json:"error"`
		RetryAfterSeconds int    `json:"retry_after_seconds"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != "sync_cooldown" || resp.RetryAfterSeconds != 2450 {
		t.Fatalf("response = %+v", resp)
	}
}

func TestWatchProviderHandlerListSyncRuns(t *testing.T) {
	startedAt := time.Date(2026, 5, 4, 16, 0, 0, 0, time.UTC)
	handler := NewWatchProviderHandler(stubWatchProviderService{
		runs: []watchsync.SyncRun{
			{
				ID:           "run-1",
				ConnectionID: "conn-1",
				Provider:     "trakt",
				Trigger:      "manual",
				Status:       string(watchsync.SyncRunStatusSuccess),
				StartedAt:    startedAt,
				CreatedAt:    startedAt,
			},
		},
	})

	req := authorizedWatchProviderRequest(http.MethodGet, "/watch-providers/trakt/sync-runs")
	rec := httptest.NewRecorder()

	handler.HandleListSyncRuns(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var resp struct {
		Runs []watchsync.SyncRun `json:"runs"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Runs) != 1 || resp.Runs[0].ID != "run-1" {
		t.Fatalf("runs = %+v", resp.Runs)
	}
}

func authorizedWatchProviderRequest(method string, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("provider", "trakt")
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = middleware.SetClaims(ctx, &auth.Claims{UserID: 7})
	ctx = middleware.SetProfileID(ctx, "profile-1")
	return req.WithContext(ctx)
}
