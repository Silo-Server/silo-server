package handlers

import (
	"errors"
	"net/http"
	"os"
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
