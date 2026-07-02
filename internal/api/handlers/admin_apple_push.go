package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

type AdminApplePushHandler struct {
	system   *notifications.System
	settings ServerSettingsStore
	client   httpDoer
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func NewAdminApplePushHandler(system *notifications.System, settings ServerSettingsStore) *AdminApplePushHandler {
	return &AdminApplePushHandler{
		system:   system,
		settings: settings,
		client:   &http.Client{Timeout: 10 * time.Second},
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

type pushRelayRegisterRequest struct {
	DeploymentID string `json:"deployment_id,omitempty"`
}

type pushRelayRegisterResponse struct {
	RequestID    string   `json:"request_id"`
	DeploymentID string   `json:"deployment_id"`
	APIKey       string   `json:"api_key"`
	KeyPrefix    string   `json:"key_prefix"`
	APNsTopics   []string `json:"apns_topics"`
}

type pushRelayNestedError struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

type adminPushRelayRegisterResponse struct {
	RelayURL         string   `json:"relay_url"`
	DeploymentID     string   `json:"deployment_id"`
	KeyPrefix        string   `json:"key_prefix"`
	APIKeyConfigured bool     `json:"api_key_configured"`
	RelayRequestID   string   `json:"relay_request_id,omitempty"`
	APNsTopics       []string `json:"apns_topics,omitempty"`
}

// HandleTest handles POST /admin/notifications/push/apple/test.
func (h *AdminApplePushHandler) HandleTest(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.system == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Apple push delivery is not available")
		return
	}
	var req adminApplePushTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	result, err := h.system.SendApplePushTest(r.Context(), req.ProfileID, req.ServerDeviceID)
	if err != nil {
		switch {
		case errors.Is(err, notifications.ErrPushDeliveryInvalid):
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		case errors.Is(err, notifications.ErrPushDeliveryNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Apple push device not found")
		case errors.Is(err, notifications.ErrPushDeliveryUnavailable):
			writeError(w, http.StatusServiceUnavailable, "unavailable", "Apple push delivery is not available")
		default:
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to send Apple push test")
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
	relayURL, err := normalizePushRelayURL(req.RelayURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	existingDeploymentID, err := h.settings.Get(r.Context(), notifications.SettingPushRelayDeploymentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "settings_error", "Failed to load relay deployment id")
		return
	}
	relayResp, err := h.registerWithRelay(r, relayURL, strings.TrimSpace(existingDeploymentID))
	if err != nil {
		status, code, message := mapRelayRegistrationError(err)
		writeError(w, status, code, message)
		return
	}
	if strings.TrimSpace(relayResp.DeploymentID) == "" || strings.TrimSpace(relayResp.APIKey) == "" {
		writeError(w, http.StatusBadGateway, "relay_bad_response", "Push relay returned an incomplete registration response")
		return
	}

	if err := h.settings.Set(r.Context(), notifications.SettingPushRelayURL, relayURL); err != nil {
		writeError(w, http.StatusInternalServerError, "settings_error", "Failed to save push relay URL")
		return
	}
	if err := h.settings.Set(r.Context(), notifications.SettingPushRelayDeploymentID, relayResp.DeploymentID); err != nil {
		writeError(w, http.StatusInternalServerError, "settings_error", "Failed to save push relay deployment id")
		return
	}
	if err := h.settings.Set(r.Context(), notifications.SettingPushRelayAPIKey, relayResp.APIKey); err != nil {
		writeError(w, http.StatusInternalServerError, "settings_error", "Failed to save push relay API key")
		return
	}
	if h.system != nil && h.system.Settings != nil {
		h.system.Settings.Invalidate(
			notifications.SettingPushRelayURL,
			notifications.SettingPushRelayDeploymentID,
			notifications.SettingPushRelayAPIKey,
		)
	}
	writeJSON(w, http.StatusOK, adminPushRelayRegisterResponse{
		RelayURL:         relayURL,
		DeploymentID:     relayResp.DeploymentID,
		KeyPrefix:        relayResp.KeyPrefix,
		APIKeyConfigured: true,
		RelayRequestID:   relayResp.RequestID,
		APNsTopics:       relayResp.APNsTopics,
	})
}

func (h *AdminApplePushHandler) registerWithRelay(r *http.Request, relayURL, deploymentID string) (*pushRelayRegisterResponse, error) {
	body, err := json.Marshal(pushRelayRegisterRequest{
		DeploymentID: deploymentID,
	})
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, relayURL+"/v1/deployments/register", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "Silo-Server/PushRelayRegistration")

	resp, err := h.client.Do(httpReq)
	if err != nil {
		return nil, relayRegistrationError{status: http.StatusBadGateway, code: "relay_unreachable", message: "Push relay could not be reached"}
	}
	defer func() { _ = resp.Body.Close() }()

	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var parsed pushRelayNestedError
		_ = json.Unmarshal(data, &parsed)
		code := parsed.Error.Code
		if code == "" {
			code = fmt.Sprintf("relay_http_%d", resp.StatusCode)
		}
		message := parsed.Error.Message
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return nil, relayRegistrationError{status: resp.StatusCode, code: code, message: message}
	}
	var parsed pushRelayRegisterResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, relayRegistrationError{status: http.StatusBadGateway, code: "relay_bad_response", message: "Push relay returned invalid JSON"}
	}
	return &parsed, nil
}

type relayRegistrationError struct {
	status  int
	code    string
	message string
}

func (e relayRegistrationError) Error() string {
	return e.code
}

func mapRelayRegistrationError(err error) (int, string, string) {
	var relayErr relayRegistrationError
	if !errors.As(err, &relayErr) {
		return http.StatusInternalServerError, "internal_error", "Failed to register push relay"
	}
	switch relayErr.status {
	case http.StatusForbidden:
		return http.StatusUnprocessableEntity, "relay_deployment_rejected", "Push relay rejected this deployment"
	case http.StatusTooManyRequests:
		return http.StatusTooManyRequests, "relay_rate_limited", relayErr.message
	case http.StatusServiceUnavailable:
		return http.StatusServiceUnavailable, "relay_unavailable", relayErr.message
	default:
		if relayErr.status >= 500 {
			return http.StatusBadGateway, "relay_error", relayErr.message
		}
		return http.StatusBadGateway, relayErr.code, relayErr.message
	}
}

// normalizePushRelayURLValue trims a relay URL and enforces https with a
// host. Empty input stays empty so callers choose their own default.
func normalizePushRelayURLValue(raw string) (string, error) {
	value := strings.TrimRight(strings.TrimSpace(raw), "/")
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", errors.New("relay_url must be an https URL")
	}
	return value, nil
}

func normalizePushRelayURL(raw string) (string, error) {
	value, err := normalizePushRelayURLValue(raw)
	if err != nil {
		return "", err
	}
	if value == "" {
		return notifications.DefaultPushRelayURL, nil
	}
	return value, nil
}
