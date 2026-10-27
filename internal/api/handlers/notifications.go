package handlers

import (
	"context"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

const (
	notificationsDefaultLimit = 25
	notificationsMaxLimit     = 100
	notificationsSyncLimit    = 50
)

// NotificationsHandler serves the profile-scoped notification inbox,
// preferences, capability, and websocket-ticket endpoints. All routes are
// mounted behind RequireProfile.
type NotificationsHandler struct {
	system *notifications.System
	hub    *evt.Hub
	// displayTokens mints the long-lived display token returned from Apple
	// push registration. Nil when JWT auth is not configured; registration
	// then omits the token and the extension falls back to the access token.
	displayTokens ApplePushDisplayTokenIssuer
}

// ApplePushDisplayTokenIssuer mints the profile-scoped token the iOS
// Notification Service extension presents to the display endpoint.
type ApplePushDisplayTokenIssuer interface {
	GenerateApplePushDisplayToken(userID int, role, sessionID, profileID string, impersonatorUserID *int) (string, time.Time, error)
}

// SetApplePushDisplayTokenIssuer wires the token issuer used by
// HandleRegisterApplePushDevice. A nil *auth.JWTService is treated as unset.
func (h *NotificationsHandler) SetApplePushDisplayTokenIssuer(issuer ApplePushDisplayTokenIssuer) {
	if jwt, ok := issuer.(*auth.JWTService); ok && jwt == nil {
		h.displayTokens = nil
		return
	}
	h.displayTokens = issuer
}

// NewNotificationsHandler creates a NotificationsHandler.
func NewNotificationsHandler(system *notifications.System, hub *evt.Hub) *NotificationsHandler {
	return &NotificationsHandler{system: system, hub: hub}
}

type notificationListResponse struct {
	Notifications []notifications.DeliveryRowPayload `json:"notifications"`
	// NextCursor pages further into the past via the `before` query param.
	// Empty when this page may be the last.
	NextCursor string `json:"next_cursor,omitempty"`
}

type notificationSyncResponse struct {
	Notifications []notifications.DeliveryRowPayload `json:"notifications"`
	NextCursor    string                             `json:"next_cursor,omitempty"`
	UnreadCount   int                                `json:"unread_count"`
}

type unreadCountResponse struct {
	Count int `json:"count"`
}

type wsTicketResponse struct {
	Ticket    string `json:"ticket"`
	ExpiresIn int    `json:"expires_in"`
}

type updatePreferencesRequest struct {
	Enabled                *bool `json:"enabled"`
	NotifyFavorites        *bool `json:"notify_favorites"`
	NotifyWatchlist        *bool `json:"notify_watchlist"`
	NotifyContinueWatching *bool `json:"notify_continue_watching"`
	NotifyNextUp           *bool `json:"notify_next_up"`
}

type capabilityResponse struct {
	InApp       capabilityInApp          `json:"in_app"`
	ApplePush   capabilityPush           `json:"apple_push"`
	AndroidPush capabilityPush           `json:"android_push"`
	WebPush     capabilityWebPush        `json:"web_push"`
	Webhooks    capabilityWebhooks       `json:"webhooks"`
	Email       capabilityAccountChannel `json:"email"`
	Discord     capabilityAccountChannel `json:"discord"`
}

// capabilityAccountChannel describes an account-level digest channel (email,
// Discord DMs).
type capabilityAccountChannel struct {
	Available bool `json:"available"`
	// Modes lists the cadences users may pick (per-episode is an admin
	// allowance); DigestHour tells the UI when daily digests go out.
	Modes      []string `json:"modes"`
	DigestHour int      `json:"digest_hour"`
}

type capabilityWebPush struct {
	Available bool   `json:"available"`
	PublicKey string `json:"public_key,omitempty"`
}

type capabilityInApp struct {
	Enabled bool `json:"enabled"`
}

type capabilityPush struct {
	Available      bool     `json:"available"`
	Provider       string   `json:"provider"`
	SupportedModes []string `json:"supported_modes"`
	// DisplayToken is true when Apple push registration returns a
	// long-lived display token for the Notification Service extension.
	// Additive; Android registration never carries one.
	DisplayToken bool `json:"display_token,omitempty"`
}

type capabilityWebhooks struct {
	Available      bool     `json:"available"`
	MaxPerProfile  int      `json:"max_per_profile"`
	SupportedTypes []string `json:"supported_types"`
}

// NotificationCapabilitiesView is shared by both API projections.
type NotificationCapabilitiesView = capabilityResponse

func (h *NotificationsHandler) NotificationCapabilities(ctx context.Context) NotificationCapabilitiesView {
	webhooks := capabilityWebhooks{Available: false, MaxPerProfile: 0, SupportedTypes: []string{}}
	if h.system.Webhooks != nil && h.system.Settings.WebhooksEnabled(ctx) {
		webhooks = capabilityWebhooks{
			Available:      true,
			MaxPerProfile:  h.system.Settings.WebhooksMaxPerProfile(ctx),
			SupportedTypes: []string{"discord", "generic"},
		}
	}
	webPush := capabilityWebPush{}
	if h.system.WebPush != nil && h.system.Settings.WebPushEnabled(ctx) {
		if publicKey, err := h.system.WebPush.PublicKey(ctx); err == nil && publicKey != "" {
			webPush = capabilityWebPush{Available: true, PublicKey: publicKey}
		}
	}
	email := capabilityAccountChannel{Modes: []string{}}
	if h.system.EmailAvailable(ctx) {
		modes := []string{notifications.ChannelModeDailyDigest}
		if h.system.Settings.EmailAllowPerEpisode(ctx) {
			modes = append(modes,
				notifications.ChannelModePerEpisode,
				notifications.ChannelModePerEpisodeAndDigest)
		}
		email = capabilityAccountChannel{
			Available:  true,
			Modes:      modes,
			DigestHour: h.system.Settings.EmailDigestHour(ctx),
		}
	}
	discordCap := capabilityAccountChannel{Modes: []string{}}
	if h.system.DiscordAvailable(ctx) {
		modes := []string{notifications.ChannelModeDailyDigest}
		if h.system.Settings.DiscordAllowPerEpisode(ctx) {
			modes = append(modes,
				notifications.ChannelModePerEpisode,
				notifications.ChannelModePerEpisodeAndDigest)
		}
		discordCap = capabilityAccountChannel{
			Available:  true,
			Modes:      modes,
			DigestHour: h.system.Settings.DiscordDigestHour(ctx),
		}
	}
	applePush := capabilityPush{Available: false, Provider: "off", SupportedModes: []string{"in_app_only"}}
	androidPush := capabilityPush{Available: false, Provider: "off", SupportedModes: []string{"in_app_only"}}
	// Like web push above, availability requires both the wiring (cipher/store)
	// and the admin delivery toggle: Available must mean "setup will actually
	// deliver", not "the server could store a token".
	if h.system.PushDevices != nil && h.system.PushDevices.Available() {
		if h.system.Settings.ApplePushDeliveryEnabled(ctx) {
			applePush = capabilityPush{
				Available:      true,
				Provider:       notifications.PushProviderSiloRelay,
				SupportedModes: []string{notifications.PushModePrivatePush, notifications.PushModeInAppOnly},
				DisplayToken:   h.displayTokens != nil,
			}
		}
		if h.system.Settings.AndroidPushDeliveryEnabled(ctx) {
			androidPush = capabilityPush{
				Available:      true,
				Provider:       notifications.PushProviderSiloRelay,
				SupportedModes: []string{notifications.PushModePrivatePush, notifications.PushModeInAppOnly},
			}
		}
	}
	return capabilityResponse{
		InApp:       capabilityInApp{Enabled: h.system.Settings.UIEnabled(ctx)},
		ApplePush:   applePush,
		AndroidPush: androidPush,
		WebPush:     webPush,
		Webhooks:    webhooks,
		Email:       email,
		Discord:     discordCap,
	}
}
