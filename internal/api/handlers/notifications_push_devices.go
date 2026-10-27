package handlers

import (
	"github.com/Silo-Server/silo-server/internal/notifications"
)

type applePushRegisterRequest struct {
	DeviceID        string `json:"device_id"`
	APNsToken       string `json:"apns_token"`
	APNsEnvironment string `json:"apns_environment"`
	APNsTopic       string `json:"apns_topic"`
	PushMode        string `json:"push_mode"`
}

type applePushRegisterResponse struct {
	ID             string `json:"id"`
	ServerDeviceID string `json:"server_device_id"`
	Enabled        bool   `json:"enabled"`
	PushMode       string `json:"push_mode"`
	// DisplayToken is a long-lived, profile-scoped credential the iOS
	// Notification Service extension presents to the display endpoint. The
	// extension cannot refresh the short-lived access token, so without this
	// every push arriving after access expiry rendered as generic text.
	// Omitted when the server cannot mint one (no JWT auth or session).
	DisplayToken          string `json:"display_token,omitempty"`
	DisplayTokenExpiresAt string `json:"display_token_expires_at,omitempty"`
}

func (h *NotificationsHandler) pushDevices() *notifications.PushDeviceService {
	if h == nil || h.system == nil {
		return nil
	}
	return h.system.PushDevices
}

type pushDeviceRegisterRequest struct {
	Platform string `json:"platform"`
	Token    string `json:"token"`
	DeviceID string `json:"device_id"`
	PushMode string `json:"push_mode"`
}
