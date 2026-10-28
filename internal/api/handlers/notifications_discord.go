package handlers

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// discordSettingsPath is the SPA page the OAuth callback redirects back to,
// with ?discord_linked=1 or ?discord_error=<reason> for the page to toast.
const discordSettingsPath = "/settings/notifications"

// DiscordNotificationsHandler serves the account-level Discord DM channel:
// preferences, the OAuth account-link flow, and the admin bot test. The
// preferences and link-init endpoints require auth; the link callback is
// public because Discord redirects the browser there without credentials —
// the one-time server-side state row authenticates it instead.
type DiscordNotificationsHandler struct {
	system       *notifications.System
	publicURL    string
	publicOrigin atomic.Pointer[string]
	Stores       userstore.UserStoreProvider
}

// MayManageDiscordNotifications applies the existing household-manager rule
// to the account-wide destination and delivery mode, before any mutation.
func (h *DiscordNotificationsHandler) MayManageDiscordNotifications(ctx context.Context, userID int, profileID string, verify func(string) error) (bool, error) {
	return mayManageHouseholdOf(ctx, h.Stores, userID, profileID, verify)
}

// NewDiscordNotificationsHandler creates a DiscordNotificationsHandler.
// publicURL may be empty, in which case linking is reported unavailable
// (Discord needs a stable redirect_uri origin).
func NewDiscordNotificationsHandler(system *notifications.System, publicURL string) *DiscordNotificationsHandler {
	h := &DiscordNotificationsHandler{system: system, publicURL: strings.TrimRight(publicURL, "/")}
	h.SetPublicURL(publicURL)
	return h
}

// SetPublicURL updates the origin used for future Discord link handshakes.
func (h *DiscordNotificationsHandler) SetPublicURL(publicURL string) {
	normalized := strings.TrimRight(strings.TrimSpace(publicURL), "/")
	h.publicOrigin.Store(&normalized)
}

// discordPreferencesResponse is the account-level Discord DM setting plus
// link state and health.
type discordPreferencesResponse struct {
	Linked          bool   `json:"linked"`
	DiscordUsername string `json:"discord_username,omitempty"`
	Mode            string `json:"mode"`
	LinkFailure     string `json:"link_failure,omitempty"`
}

type updateDiscordPreferencesRequest struct {
	Mode string `json:"mode"`
}

type discordLinkInitResponse struct {
	URL string `json:"url"`
}

type discordTestResponse struct {
	OK         bool   `json:"ok"`
	DurationMS int64  `json:"duration_ms"`
	Message    string `json:"message"`
}
