package handlers

import (
	"time"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

// webPushSubscriptionResponse is the API view of a browser push
// registration. The keys are write-only: clients re-subscribe rather than
// read them back.
type webPushSubscriptionResponse struct {
	ID            string     `json:"id"`
	Endpoint      string     `json:"endpoint"`
	DeviceName    string     `json:"device_name,omitempty"`
	Enabled       bool       `json:"enabled"`
	CreatedAt     time.Time  `json:"created_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	LastFailureAt *time.Time `json:"last_failure_at"`
}

func (h *NotificationsHandler) webPush() *notifications.WebPushService {
	if h == nil || h.system == nil {
		return nil
	}
	return h.system.WebPush
}

type webPushSubscribeRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	DeviceName string `json:"device_name"`
}

type webPushUnsubscribeRequest struct {
	Endpoint string `json:"endpoint"`
}
