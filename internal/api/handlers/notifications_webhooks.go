package handlers

import (
	"time"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

// webhookResponse is the API view of a webhook. It never includes the
// destination URL (Discord webhook tokens are bearer credentials in the URL
// path) or the signing secret — only url_host for identification.
type webhookResponse struct {
	ID                     string     `json:"id"`
	Name                   string     `json:"name"`
	Type                   string     `json:"type"`
	URLHost                string     `json:"url_host"`
	Enabled                bool       `json:"enabled"`
	NotifyFavorites        bool       `json:"notify_favorites"`
	NotifyWatchlist        bool       `json:"notify_watchlist"`
	NotifyContinueWatching bool       `json:"notify_continue_watching"`
	NotifyNextUp           bool       `json:"notify_next_up"`
	NotifyRequests         bool       `json:"notify_requests"`
	ConsecutiveFailures    int        `json:"consecutive_failures"`
	DisabledReason         *string    `json:"disabled_reason"`
	LastSuccessAt          *time.Time `json:"last_success_at"`
	LastFailureAt          *time.Time `json:"last_failure_at"`
	LastFailureStatus      *int       `json:"last_failure_status"`
	LastFailureMessage     *string    `json:"last_failure_message"`
	// SigningSecret is present only in create / rotate-secret responses.
	SigningSecret string `json:"signing_secret,omitempty"`
}

type webhookRequest struct {
	Name                   *string `json:"name"`
	URL                    *string `json:"url"`
	Type                   *string `json:"type"`
	Enabled                *bool   `json:"enabled"`
	NotifyFavorites        *bool   `json:"notify_favorites"`
	NotifyWatchlist        *bool   `json:"notify_watchlist"`
	NotifyContinueWatching *bool   `json:"notify_continue_watching"`
	NotifyNextUp           *bool   `json:"notify_next_up"`
	NotifyRequests         *bool   `json:"notify_requests"`
}

func (h *NotificationsHandler) webhooks() *notifications.WebhookService {
	if h == nil || h.system == nil {
		return nil
	}
	return h.system.Webhooks
}
