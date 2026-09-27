package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/Silo-Server/silo-server/internal/discord"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

const (
	discordLinkScope           = "scope"
	discordLinkCode            = "code"
	discordLinkState           = "state"
	discordLinkDiscordError    = "discord_error"
	discordLinkDiscordLinked   = "discord_linked"
	discordLinkDisabled        = "disabled"
	discordLinkDenied          = "denied"
	discordLinkStateInvalid    = "state_invalid"
	discordLinkInvalidCallback = "invalid_callback"
	discordLinkExchangeFailed  = "exchange_failed"
)

const notificationDiscordCallbackV1 = "/api/v1/notifications/discord/link/callback"
const notificationDiscordCallbackV2 = "/api/v2/notifications/discord/link/callback"

type NotificationDiscordLinkBackend interface {
	DiscordAvailable(context.Context) bool
	BeginDiscordLink(context.Context, string, int) error
	ConsumeDiscordLinkState(context.Context, string) (int, bool, error)
	CompleteDiscordLink(context.Context, int, string, string) (discord.User, error)
}

type DiscordLinkHandler struct {
	backend      NotificationDiscordLinkBackend
	settings     *notifications.Settings
	publicURL    string
	publicOrigin atomic.Pointer[string]
}

func NewDiscordLinkHandler(backend NotificationDiscordLinkBackend, settings *notifications.Settings, publicURL string) *DiscordLinkHandler {
	h := &DiscordLinkHandler{backend: backend, settings: settings, publicURL: strings.TrimRight(publicURL, "/")}
	h.SetPublicURL(publicURL)
	return h
}

func (h *DiscordLinkHandler) currentPublicURL() string {
	if value := h.publicOrigin.Load(); value != nil {
		return *value
	}
	return h.publicURL
}

// SetPublicURL updates the origin used for future Discord link handshakes.
func (h *DiscordLinkHandler) SetPublicURL(publicURL string) {
	normalized := strings.TrimRight(strings.TrimSpace(publicURL), "/")
	h.publicOrigin.Store(&normalized)
}
func (h *DiscordNotificationsHandler) linkHandler() *DiscordLinkHandler {
	var settings *notifications.Settings
	if h.system != nil {
		settings = h.system.Settings
	}
	return NewDiscordLinkHandler(h.system, settings, h.currentPublicURL())
}

func (h *DiscordLinkHandler) beginLink(ctx context.Context, userID int, callbackPath string) (string, error) {
	if h.backend == nil || !h.backend.DiscordAvailable(ctx) {
		return "", apiError(409, "not_configured", "Discord integration is not enabled by the administrator")
	}
	publicURL := h.currentPublicURL()
	if publicURL == "" {
		return "", apiError(409, "no_public_url", "Linking requires the Silo public URL to be configured")
	}
	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", apiError(500, "internal_error", "Failed to start Discord link")
	}
	state := hex.EncodeToString(stateBytes)
	if err := h.backend.BeginDiscordLink(ctx, state, userID); err != nil {
		return "", apiError(500, "internal_error", "Failed to start Discord link")
	}
	query := url.Values{"client_id": {h.settings.DiscordClientID(ctx)}, "response_type": {discordLinkCode}, discordLinkScope: {"identify"}, "redirect_uri": {publicURL + callbackPath}, discordLinkState: {state}}
	return discord.AuthorizeURL + "?" + query.Encode(), nil
}

func (h *DiscordLinkHandler) BeginNotificationDiscordLink(ctx context.Context, userID int) (string, error) {
	return h.beginLink(ctx, userID, notificationDiscordCallbackV2)
}
func (h *DiscordLinkHandler) HandleNotificationDiscordCallback(w http.ResponseWriter, r *http.Request) {
	h.handleCallback(w, r, notificationDiscordCallbackV2)
}
func (h *DiscordLinkHandler) handleCallback(w http.ResponseWriter, r *http.Request, callbackPath string) {
	redirectBack := func(params url.Values) {
		http.Redirect(w, r, discordSettingsPath+"?"+params.Encode(), http.StatusFound)
	}
	if h.backend == nil || !h.backend.DiscordAvailable(r.Context()) {
		redirectBack(url.Values{discordLinkDiscordError: {discordLinkDisabled}})
		return
	}
	query := r.URL.Query()
	if query.Get("error") != "" {
		redirectBack(url.Values{discordLinkDiscordError: {discordLinkDenied}})
		return
	}
	state, code := query.Get(discordLinkState), query.Get(discordLinkCode)
	if state == "" || code == "" {
		redirectBack(url.Values{discordLinkDiscordError: {discordLinkInvalidCallback}})
		return
	}
	userID, ok, err := h.backend.ConsumeDiscordLinkState(r.Context(), state)
	if err != nil || !ok {
		redirectBack(url.Values{discordLinkDiscordError: {discordLinkStateInvalid}})
		return
	}
	if _, err = h.backend.CompleteDiscordLink(r.Context(), userID, code, h.currentPublicURL()+callbackPath); err != nil {
		redirectBack(url.Values{discordLinkDiscordError: {discordLinkExchangeFailed}})
		return
	}
	redirectBack(url.Values{discordLinkDiscordLinked: {"1"}})
}
