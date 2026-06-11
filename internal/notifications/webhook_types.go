package notifications

import (
	"encoding/json"
	"time"
)

// Webhook types.
const (
	WebhookTypeDiscord = "discord"
	WebhookTypeGeneric = "generic"
)

// Webhook attempt outcomes.
const (
	WebhookOutcomePending      = "pending"
	WebhookOutcomeDelivered    = "delivered"
	WebhookOutcomeRetrying     = "retrying"
	WebhookOutcomeFailed       = "failed"
	WebhookOutcomeAutoDisabled = "auto_disabled"
)

// DeliveryTypeWebhookAutoDisabled is the operational in-app notice posted
// when a webhook is auto-disabled. Its reason_flags carry
// {"webhook_id","webhook_name","last_failure_status"} instead of reason
// booleans, and it must never itself enqueue webhook attempts (loop guard).
const DeliveryTypeWebhookAutoDisabled = "webhook.auto_disabled"

// genericNotificationTitle is the display fallback for delivery types this
// build does not know (the type registry is extensible).
const genericNotificationTitle = "Silo notification"

// genericEpisodeTitle is the display fallback for episode.available rows
// whose catalog metadata is missing or was deleted.
const genericEpisodeTitle = "New episode"

// schemeHTTPS is the only scheme outbound notification HTTP traffic may use.
const schemeHTTPS = "https"

// Webhook is a profile-scoped outbound destination. URL and signing secret
// are stored as enc:v1: envelopes and never leave the server.
type Webhook struct {
	ID                      string
	UserID                  int
	ProfileID               string
	Name                    string
	Type                    string
	URLCiphertext           string
	URLHost                 string
	SigningSecretCiphertext *string
	Enabled                 bool
	NotifyFavorites         bool
	NotifyWatchlist         bool
	NotifyContinueWatching  bool
	NotifyNextUp            bool
	NotifyRequests          bool
	ConsecutiveFailures     int
	DisabledReason          *string
	LastSuccessAt           *time.Time
	LastFailureAt           *time.Time
	LastFailureStatus       *int
	LastFailureMessage      *string
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

// MatchesReasons reports whether at least one of the delivery's matched
// reasons passes this webhook's per-reason filter. Per-webhook flags narrow
// what fires for a destination; they can never re-enable a reason the profile
// disabled globally (those deliveries are never created).
func (w Webhook) MatchesReasons(flags ReasonFlags) bool {
	return (flags.Favorite && w.NotifyFavorites) ||
		(flags.Watchlist && w.NotifyWatchlist) ||
		(flags.ContinueWatching && w.NotifyContinueWatching) ||
		(flags.NextUp && w.NotifyNextUp)
}

// DeliveryAttempt is one row of a per-target dispatch outbox / retry log.
// TargetID is the channel-specific destination: a notification_webhooks.id
// for the webhook channel, a web_push_subscriptions.id for web push.
type DeliveryAttempt struct {
	ID                     string
	NotificationDeliveryID string
	TargetID               string
	AttemptNumber          int
	AttemptedAt            time.Time
	NextRetryAt            *time.Time
	HTTPStatus             *int
	Outcome                string
	FailureMessage         *string
}

// parseReasonFlags decodes a delivery's reason_flags JSONB for
// episode.available rows. Operational types carry different shapes and decode
// to the zero value.
func parseReasonFlags(raw []byte) ReasonFlags {
	var flags ReasonFlags
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &flags)
	}
	return flags
}

// reasonLabelList renders matched reasons as display labels, in stable
// precedence order.
func reasonLabelList(flags ReasonFlags) []string {
	labels := make([]string, 0, 4)
	if flags.Favorite {
		labels = append(labels, "Favorited")
	}
	if flags.Watchlist {
		labels = append(labels, "Watchlisted")
	}
	if flags.ContinueWatching {
		labels = append(labels, "Continue Watching")
	}
	if flags.NextUp {
		labels = append(labels, "Next Up")
	}
	return labels
}
