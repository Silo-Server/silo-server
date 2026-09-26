package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

type AdminApplePushHandler struct {
	system              *notifications.System
	settings            ServerSettingsStore
	client              httpDoer
	developmentRelayURL string
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func NewAdminApplePushHandler(system *notifications.System, settings ServerSettingsStore) *AdminApplePushHandler {
	return &AdminApplePushHandler{
		system:              system,
		settings:            settings,
		client:              &http.Client{Timeout: 10 * time.Second},
		developmentRelayURL: os.Getenv("SILO_PUSH_RELAY_DEVELOPMENT_URL"),
	}
}

type adminApplePushTestRequest struct {
	ProfileID      string `json:"profile_id"`
	ServerDeviceID string `json:"server_device_id"`
}

type adminApplePushTestResponse struct {
	AttemptID      string `json:"attempt_id"`
	PushDeviceID   string `json:"push_device_id"`
	ServerDeviceID string `json:"server_device_id"`
	Outcome        string `json:"outcome"`
	RelayRequestID string `json:"relay_request_id,omitempty"`
	UpstreamStatus *int   `json:"upstream_status,omitempty"`
	UpstreamReason string `json:"upstream_reason,omitempty"`
	FailureMessage string `json:"failure_message,omitempty"`
}

type adminPushRelayRegisterRequest struct {
	RelayURL string `json:"relay_url"`
}

type adminPushRelayRegisterResponse struct {
	RelayURL         string   `json:"relay_url"`
	DeploymentID     string   `json:"deployment_id"`
	KeyPrefix        string   `json:"key_prefix"`
	APIKeyConfigured bool     `json:"api_key_configured"`
	RelayRequestID   string   `json:"relay_request_id,omitempty"`
	APNsTopics       []string `json:"apns_topics,omitempty"`
	ExpiresAt        string   `json:"expires_at"`
}

// HandleTest handles POST /admin/notifications/push/apple/test.
func (h *AdminApplePushHandler) HandleTest(w http.ResponseWriter, r *http.Request) {
	h.handleTest(w, r, "Apple", func(ctx context.Context, profileID, serverDeviceID string) (*notifications.ApplePushTestResult, error) {
		return h.system.SendApplePushTest(ctx, profileID, serverDeviceID)
	})
}

// HandleTestAndroid handles POST /admin/notifications/push/fcm/test.
func (h *AdminApplePushHandler) HandleTestAndroid(w http.ResponseWriter, r *http.Request) {
	h.handleTest(w, r, "Android", func(ctx context.Context, profileID, serverDeviceID string) (*notifications.ApplePushTestResult, error) {
		return h.system.SendAndroidPushTest(ctx, profileID, serverDeviceID)
	})
}

func (h *AdminApplePushHandler) handleTest(w http.ResponseWriter, r *http.Request, platformLabel string, send func(context.Context, string, string) (*notifications.ApplePushTestResult, error)) {
	if h == nil || h.system == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", platformLabel+" push delivery is not available")
		return
	}
	var req adminApplePushTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	result, err := send(r.Context(), req.ProfileID, req.ServerDeviceID)
	if err != nil {
		switch {
		case errors.Is(err, notifications.ErrPushDeliveryInvalid):
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		case errors.Is(err, notifications.ErrPushDeliveryNotFound):
			writeError(w, http.StatusNotFound, "not_found", platformLabel+" push device not found")
		case errors.Is(err, notifications.ErrPushDeliveryUnavailable):
			writeError(w, http.StatusServiceUnavailable, "unavailable", platformLabel+" push delivery is not available")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to send "+platformLabel+" push test")
		}
		return
	}
	writeJSON(w, http.StatusOK, adminApplePushTestResponse{
		AttemptID:      result.AttemptID,
		PushDeviceID:   result.PushDeviceID,
		ServerDeviceID: result.ServerDeviceID,
		Outcome:        result.Outcome,
		RelayRequestID: result.RelayRequestID,
		UpstreamStatus: result.UpstreamStatus,
		UpstreamReason: result.UpstreamReason,
		FailureMessage: result.FailureMessage,
	})
}

// HandleRegisterRelay handles POST /admin/notifications/push/relay/register.
func (h *AdminApplePushHandler) HandleRegisterRelay(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.settings == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Settings store is not available")
		return
	}
	var req adminPushRelayRegisterRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	view, err := h.RegisterNotificationRelay(r.Context(), req.RelayURL)
	if err != nil {
		writeNotificationRelayFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, adminPushRelayRegisterResponse{
		RelayURL: view.RelayURL, DeploymentID: view.DeploymentID, KeyPrefix: view.KeyPrefix,
		APIKeyConfigured: true, RelayRequestID: view.RelayRequestID, APNsTopics: view.APNsTopics,
		ExpiresAt: view.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// HandleClearRelay handles DELETE /admin/notifications/push/relay. Clearing
// the local capability is deliberately explicit: it lets an administrator
// change relay origins or recover from a revoked deployment without exposing
// credential fields through the generic settings endpoint.
func (h *AdminApplePushHandler) HandleClearRelay(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.settings == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Settings store is not available")
		return
	}

	if err := h.ClearNotificationRelay(r.Context()); err != nil {
		writeNotificationRelayFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeNotificationRelayFailure(w http.ResponseWriter, err error) {
	if apiErr, ok := errors.AsType[*APIError](err); ok {
		if apiErr.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(apiErr.RetryAfter))
		}
		writeError(w, apiErr.Status, apiErr.Code, apiErr.Message)
		return
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "Failed to register push relay")
}

func mapRelayRegistrationError(err error) (int, string, string) {
	var relayErr notifications.RelayCredentialError
	if !errors.As(err, &relayErr) {
		return http.StatusInternalServerError, "internal_error", "Failed to register push relay"
	}
	switch relayErr.Status {
	case http.StatusForbidden:
		return http.StatusUnprocessableEntity, "relay_deployment_rejected", "Push relay rejected this deployment"
	case http.StatusTooManyRequests:
		return http.StatusTooManyRequests, "relay_rate_limited", relayErr.Message
	case http.StatusServiceUnavailable:
		return http.StatusServiceUnavailable, "relay_unavailable", relayErr.Message
	default:
		if relayErr.Status >= 500 {
			return http.StatusBadGateway, "relay_error", relayErr.Message
		}
		return http.StatusBadGateway, relayErr.Code, relayErr.Message
	}
}
