package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

type WatchProviderService interface {
	ListProviders() []watchsync.ProviderSummary
	StartDeviceAuth(ctx context.Context, userID int, profileID string, providerKey string) (watchsync.DeviceAuthSession, error)
	PollDeviceAuth(ctx context.Context, userID int, profileID string, providerKey string, sessionID string) (watchsync.Connection, error)
	ConnectAPIKey(ctx context.Context, userID int, profileID string, providerKey string, apiKey string) (watchsync.Connection, error)
	GetConnectionStatus(ctx context.Context, userID int, profileID string, provider string) (watchsync.ConnectionStatus, error)
	UpdateConnection(ctx context.Context, userID int, profileID string, provider string, update watchsync.ConnectionUpdate) (watchsync.ConnectionStatus, error)
	DeleteConnection(ctx context.Context, userID int, profileID string, provider string) error
	RequestManualSync(ctx context.Context, userID int, profileID string, provider string) (watchsync.ManualSyncResult, error)
	ListSyncRuns(ctx context.Context, userID int, profileID string, provider string, limit int) ([]watchsync.SyncRun, error)
}

type WatchProviderHandler struct {
	service WatchProviderService
}

func NewWatchProviderHandler(service WatchProviderService) *WatchProviderHandler {
	return &WatchProviderHandler{service: service}
}

func (h *WatchProviderHandler) HandleListProviders(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Watch provider service is not configured")
		return
	}

	providers := h.service.ListProviders()
	if providers == nil {
		providers = []watchsync.ProviderSummary{}
	}

	writeJSON(w, http.StatusOK, struct {
		Providers []watchsync.ProviderSummary `json:"providers"`
	}{
		Providers: providers,
	})
}

func (h *WatchProviderHandler) HandleGetConnection(w http.ResponseWriter, r *http.Request) {
	status, ok := h.connectionStatus(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *WatchProviderHandler) HandleUpdateConnection(w http.ResponseWriter, r *http.Request) {
	userID, profileID, provider, ok := watchProviderRequestScope(w, r)
	if !ok {
		return
	}
	var update watchsync.ConnectionUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	status, err := h.service.UpdateConnection(r.Context(), userID, profileID, provider, update)
	if err != nil {
		writeError(w, http.StatusBadRequest, "watch_provider_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *WatchProviderHandler) HandleDeleteConnection(w http.ResponseWriter, r *http.Request) {
	userID, profileID, provider, ok := watchProviderRequestScope(w, r)
	if !ok {
		return
	}
	if err := h.service.DeleteConnection(r.Context(), userID, profileID, provider); err != nil {
		writeError(w, http.StatusBadRequest, "watch_provider_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *WatchProviderHandler) HandleStartDeviceAuth(w http.ResponseWriter, r *http.Request) {
	userID, profileID, provider, ok := watchProviderRequestScope(w, r)
	if !ok {
		return
	}
	session, err := h.service.StartDeviceAuth(r.Context(), userID, profileID, provider)
	if err != nil {
		writeError(w, http.StatusBadRequest, "watch_provider_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (h *WatchProviderHandler) HandlePollDeviceAuth(w http.ResponseWriter, r *http.Request) {
	userID, profileID, provider, ok := watchProviderRequestScope(w, r)
	if !ok {
		return
	}
	var req struct {
		AuthSessionID string `json:"auth_session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if _, err := h.service.PollDeviceAuth(r.Context(), userID, profileID, provider, req.AuthSessionID); err != nil {
		writeError(w, http.StatusBadRequest, "watch_provider_error", err.Error())
		return
	}
	status, err := h.service.GetConnectionStatus(r.Context(), userID, profileID, provider)
	if err != nil {
		writeError(w, http.StatusBadRequest, "watch_provider_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *WatchProviderHandler) HandleConnectAPIKey(w http.ResponseWriter, r *http.Request) {
	userID, profileID, provider, ok := watchProviderRequestScope(w, r)
	if !ok {
		return
	}
	var req struct {
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if _, err := h.service.ConnectAPIKey(r.Context(), userID, profileID, provider, req.APIKey); err != nil {
		writeError(w, http.StatusBadRequest, "watch_provider_error", err.Error())
		return
	}
	status, err := h.service.GetConnectionStatus(r.Context(), userID, profileID, provider)
	if err != nil {
		writeError(w, http.StatusBadRequest, "watch_provider_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *WatchProviderHandler) HandleManualSync(w http.ResponseWriter, r *http.Request) {
	userID, profileID, provider, ok := watchProviderRequestScope(w, r)
	if !ok {
		return
	}
	result, err := h.service.RequestManualSync(r.Context(), userID, profileID, provider)
	if err != nil {
		var cooldown watchsync.SyncCooldownError
		if errors.As(err, &cooldown) {
			w.Header().Set("Retry-After", strconv.Itoa(cooldown.RetryAfterSeconds))
			writeJSON(w, http.StatusTooManyRequests, struct {
				Error             string `json:"error"`
				Message           string `json:"message"`
				RetryAfterSeconds int    `json:"retry_after_seconds"`
			}{
				Error:             "sync_cooldown",
				Message:           "Watch provider sync recently ran. Try again later.",
				RetryAfterSeconds: cooldown.RetryAfterSeconds,
			})
			return
		}
		writeError(w, http.StatusBadRequest, "watch_provider_error", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (h *WatchProviderHandler) HandleListSyncRuns(w http.ResponseWriter, r *http.Request) {
	userID, profileID, provider, ok := watchProviderRequestScope(w, r)
	if !ok {
		return
	}
	limit := 10
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid limit")
			return
		}
		limit = parsed
	}
	runs, err := h.service.ListSyncRuns(r.Context(), userID, profileID, provider, limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, "watch_provider_error", err.Error())
		return
	}
	if runs == nil {
		runs = []watchsync.SyncRun{}
	}
	writeJSON(w, http.StatusOK, struct {
		Runs []watchsync.SyncRun `json:"runs"`
	}{Runs: runs})
}

func (h *WatchProviderHandler) connectionStatus(w http.ResponseWriter, r *http.Request) (watchsync.ConnectionStatus, bool) {
	userID, profileID, provider, ok := watchProviderRequestScope(w, r)
	if !ok {
		return watchsync.ConnectionStatus{}, false
	}
	status, err := h.service.GetConnectionStatus(r.Context(), userID, profileID, provider)
	if err != nil {
		writeError(w, http.StatusBadRequest, "watch_provider_error", err.Error())
		return watchsync.ConnectionStatus{}, false
	}
	return status, true
}

func watchProviderRequestScope(w http.ResponseWriter, r *http.Request) (int, string, string, bool) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	provider, err := decodedURLParam(r, "provider")
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid provider name")
		return 0, "", "", false
	}
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return 0, "", "", false
	}
	if profileID == "" {
		writeError(w, http.StatusBadRequest, "profile_required", "Profile is required")
		return 0, "", "", false
	}
	if provider == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Provider is required")
		return 0, "", "", false
	}
	return userID, profileID, provider, true
}
