package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
