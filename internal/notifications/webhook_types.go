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
	Revision                int64
	ID                      string
	UserID                  int
	ProfileID               string
	Name                    string
	Type                    string
	URLCiphertext           string
	URLHost                 string
	SigningSecretCiphertext *string
	Enabled                 bool
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

// MatchesReasons reports whether the delivery's matched reasons pass this
// webhook's per-reason filter: Next Up, the only reason, with notify_next_up
// on. Per-webhook flags narrow what fires for a destination; they can never
// re-enable a reason the profile disabled globally (those deliveries are never
// created). A delivery from before Next Up was the only reason that matched
// only a retired reason does not pass.
func (w Webhook) MatchesReasons(flags ReasonFlags) bool {
	return flags.NextUp && w.NotifyNextUp
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
// to the zero value, and the favorite, watchlist and continue_watching keys of
// rows written before Next Up was the only reason are ignored.
func parseReasonFlags(raw []byte) ReasonFlags {
	var flags ReasonFlags
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &flags)
	}
	return flags
}

// reasonLabelList renders matched reasons as display labels.
func reasonLabelList(flags ReasonFlags) []string {
	if flags.NextUp {
		return []string{"Next Up"}
	}
	return []string{}
}
