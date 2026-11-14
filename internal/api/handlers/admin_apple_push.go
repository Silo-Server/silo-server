package handlers

import (
	"context"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

// adminPushTestSender dispatches a test push to a device on userID's account.
type adminPushTestSender interface {
	SendApplePushTest(ctx context.Context, userID int, profileID, serverDeviceID string) (*notifications.ApplePushTestResult, error)
	SendAndroidPushTest(ctx context.Context, userID int, profileID, serverDeviceID string) (*notifications.ApplePushTestResult, error)
}

type AdminApplePushHandler struct {
	system              *notifications.System
	pushTests           adminPushTestSender
	settings            ServerSettingsStore
	client              httpDoer
	developmentRelayURL string
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func NewAdminApplePushHandler(system *notifications.System, settings ServerSettingsStore) *AdminApplePushHandler {
	h := &AdminApplePushHandler{
		system:              system,
		settings:            settings,
		client:              &http.Client{Timeout: 10 * time.Second},
		developmentRelayURL: os.Getenv("SILO_PUSH_RELAY_DEVELOPMENT_URL"),
	}
	if system != nil {
		h.pushTests = system
	}
	return h
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
