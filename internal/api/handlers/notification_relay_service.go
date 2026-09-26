package handlers

import (
	"context"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

// NotificationRelayView omits the reusable relay credential.
type NotificationRelayView struct {
	RelayURL, DeploymentID, KeyPrefix, RelayRequestID string
	APNsTopics                                        []string
	ExpiresAt                                         time.Time
}

func (h *AdminApplePushHandler) RegisterNotificationRelay(ctx context.Context, requestedURL string) (NotificationRelayView, error) {
	if h == nil || h.settings == nil {
		return NotificationRelayView{}, apiError(503, "unavailable", "Settings store is not available")
	}
	relayURL, err := notifications.NormalizePushRelayURL(requestedURL, h.developmentRelayURL)
	if err != nil {
		return NotificationRelayView{}, apiError(400, "bad_request", err.Error())
	}
	settings := notifications.NewSettings(h.settings)
	if h.system != nil && h.system.Settings != nil {
		settings = h.system.Settings
	}
	current := notifications.LoadPushRelayCredential(ctx, settings)
	var response notifications.RelayCredentialResult
	switch {
	case current.APIKey == "", notifications.IsLegacyPushRelayKey(current.APIKey), current.ReregistrationRequired:
		// Explicit re-registration must replace a rejected capability; a first
		// registration yields to one that a replica's first send just stored.
		var registered bool
		response, registered, err = notifications.RegisterRelayCredentialIfAbsent(ctx, settings, h.client, relayURL, current.ReregistrationRequired)
		if err == nil && !registered {
			if response.Credential.ReregistrationRequired || response.Credential.APIKey == "" {
				return NotificationRelayView{}, apiError(409, "relay_reregistration_required", "The relay credential was cleared concurrently; explicit re-registration is required")
			}
			// A replica's first send won. Only report success if it landed on
			// the origin the administrator asked for.
			storedURL, urlErr := notifications.NormalizePushRelayURL(response.Credential.RelayURL, h.developmentRelayURL)
			if urlErr != nil || storedURL != relayURL {
				return NotificationRelayView{}, apiError(409, "relay_origin_change_requires_reregistration", "A credential for a different relay origin was registered concurrently; clear it before changing relay origins")
			}
		}
	default:
		currentURL, urlErr := notifications.NormalizePushRelayURL(current.RelayURL, h.developmentRelayURL)
		if urlErr != nil || currentURL != relayURL {
			return NotificationRelayView{}, apiError(409, "relay_origin_change_requires_reregistration", "Clear or re-register the relay credential before changing relay origins")
		}
		current.RelayURL = currentURL
		response, err = notifications.RotateRelayCredential(ctx, settings, h.client, current)
	}
	if err != nil {
		relayErr, relayFailure := errors.AsType[notifications.RelayCredentialError](err)
		if current.APIKey != "" && !notifications.IsLegacyPushRelayKey(current.APIKey) && relayFailure && relayErr.Status == http.StatusUnauthorized {
			if markErr := notifications.MarkRelayReregistrationRequired(ctx, settings, current); markErr != nil {
				return NotificationRelayView{}, apiError(500, "settings_error", "Failed to save push relay credential status")
			}
			return NotificationRelayView{}, apiError(409, "relay_reregistration_required", "The current relay capability was rejected; explicit re-registration is required")
		}
		status, code, message := mapRelayRegistrationError(err)
		failure := apiError(status, code, message)
		if relayFailure && relayErr.RetryAfter > 0 {
			failure.RetryAfter = max(1, int(math.Ceil(relayErr.RetryAfter.Seconds())))
		}
		return NotificationRelayView{}, failure
	}
	c := response.Credential
	return NotificationRelayView{RelayURL: c.RelayURL, DeploymentID: c.DeploymentID, KeyPrefix: c.KeyPrefix, RelayRequestID: response.RequestID, APNsTopics: response.APNsTopics, ExpiresAt: c.ExpiresAt}, nil
}

func (h *AdminApplePushHandler) ClearNotificationRelay(ctx context.Context) error {
	if h == nil || h.settings == nil {
		return apiError(503, "unavailable", "Settings store is not available")
	}
	settings := notifications.NewSettings(h.settings)
	if h.system != nil && h.system.Settings != nil {
		settings = h.system.Settings
	}
	// Clearing is an administrator decision that delivery must not undo:
	// the empty credential carries the re-registration marker so first-use
	// registration stays parked until an explicit register.
	if err := settings.UpdatePushRelayCredential(ctx, notifications.PushRelayCredential{ReregistrationRequired: true}); err != nil {
		return apiError(500, "settings_error", "Failed to clear push relay credential")
	}
	return nil
}
