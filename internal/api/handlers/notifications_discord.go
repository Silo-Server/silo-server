package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/discord"
	"github.com/Silo-Server/silo-server/internal/notifications"
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
	system    *notifications.System
	publicURL string
}

// NewDiscordNotificationsHandler creates a DiscordNotificationsHandler.
// publicURL may be empty, in which case linking is reported unavailable
// (Discord needs a stable redirect_uri origin).
func NewDiscordNotificationsHandler(system *notifications.System, publicURL string) *DiscordNotificationsHandler {
	return &DiscordNotificationsHandler{system: system, publicURL: strings.TrimRight(publicURL, "/")}
}

func (h *DiscordNotificationsHandler) redirectURI() string {
	return h.publicURL + "/api/v1/notifications/discord/link/callback"
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

func discordPrefsResponse(prefs notifications.DiscordPrefs) discordPreferencesResponse {
	return discordPreferencesResponse{
		Linked:          prefs.Linked(),
		DiscordUsername: prefs.DiscordUsername,
		Mode:            prefs.Mode,
		LinkFailure:     prefs.LinkFailure,
	}
}

// HandleGetPreferences handles GET /notifications/discord-preferences.
func (h *DiscordNotificationsHandler) HandleGetPreferences(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	prefs, err := h.system.DiscordPrefsFor(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load Discord preferences")
		return
	}
	writeJSON(w, http.StatusOK, discordPrefsResponse(prefs))
}

// HandleUpdatePreferences handles PUT /notifications/discord-preferences.
func (h *DiscordNotificationsHandler) HandleUpdatePreferences(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())

	var req updateDiscordPreferencesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	err := h.system.SetDiscordMode(r.Context(), userID, req.Mode)
	switch {
	case err == nil:
	case errors.Is(err, notifications.ErrDiscordModeInvalid):
		writeError(w, http.StatusBadRequest, "bad_request", "Unknown Discord notification mode")
		return
	case errors.Is(err, notifications.ErrDiscordModeNotAllowed):
		writeError(w, http.StatusBadRequest, "not_allowed", "Per-episode Discord DMs are disabled by the administrator")
		return
	case errors.Is(err, notifications.ErrDiscordNotLinked):
		writeError(w, http.StatusBadRequest, "not_linked", "Link a Discord account first")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save Discord preferences")
		return
	}
	prefs, err := h.system.DiscordPrefsFor(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load Discord preferences")
		return
	}
	writeJSON(w, http.StatusOK, discordPrefsResponse(prefs))
}

// HandleUnlink handles DELETE /notifications/discord-link: removes the
// linked identity and switches the channel off.
func (h *DiscordNotificationsHandler) HandleUnlink(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if err := h.system.UnlinkDiscord(r.Context(), userID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to unlink Discord account")
		return
	}
	writeJSON(w, http.StatusOK, discordPreferencesResponse{Mode: notifications.ChannelModeOff})
}

type discordLinkInitResponse struct {
	URL string `json:"url"`
}

// HandleLinkInit handles POST /notifications/discord/link/init: records a
// one-time state for the signed-in account and returns the Discord consent
// URL for the SPA to navigate to.
func (h *DiscordNotificationsHandler) HandleLinkInit(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if !h.system.DiscordAvailable(r.Context()) {
		writeError(w, http.StatusConflict, "not_configured", "Discord integration is not enabled by the administrator")
		return
	}
	if h.publicURL == "" {
		writeError(w, http.StatusConflict, "no_public_url", "Linking requires SILO_PUBLIC_URL to be configured")
		return
	}

	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to start Discord link")
		return
	}
	state := hex.EncodeToString(stateBytes)
	if err := h.system.BeginDiscordLink(r.Context(), state, userID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to start Discord link")
		return
	}

	query := url.Values{
		"client_id":     {h.system.Settings.DiscordClientID(r.Context())},
		"response_type": {"code"},
		"scope":         {"identify"},
		"redirect_uri":  {h.redirectURI()},
		"state":         {state},
	}
	writeJSON(w, http.StatusOK, discordLinkInitResponse{
		URL: discord.AuthorizeURL + "?" + query.Encode(),
	})
}

// HandleLinkCallback handles GET /notifications/discord/link/callback.
// Public route: Discord redirects the user's browser here. The one-time
// state row authenticates the request and recovers which account started
// the flow; the browser is then sent back to the settings page.
func (h *DiscordNotificationsHandler) HandleLinkCallback(w http.ResponseWriter, r *http.Request) {
	redirectBack := func(params url.Values) {
		http.Redirect(w, r, discordSettingsPath+"?"+params.Encode(), http.StatusFound)
	}

	// The admin may have switched the integration off between init and
	// callback; don't complete a link for a disabled channel.
	if !h.system.DiscordAvailable(r.Context()) {
		redirectBack(url.Values{"discord_error": {"disabled"}})
		return
	}

	query := r.URL.Query()
	if query.Get("error") != "" {
		// The user declined on Discord's consent screen.
		redirectBack(url.Values{"discord_error": {"denied"}})
		return
	}
	state := query.Get("state")
	code := query.Get("code")
	if state == "" || code == "" {
		redirectBack(url.Values{"discord_error": {"invalid_callback"}})
		return
	}

	userID, ok, err := h.system.ConsumeDiscordLinkState(r.Context(), state)
	if err != nil || !ok {
		redirectBack(url.Values{"discord_error": {"state_invalid"}})
		return
	}
	if _, err := h.system.CompleteDiscordLink(r.Context(), userID, code, h.redirectURI()); err != nil {
		redirectBack(url.Values{"discord_error": {"exchange_failed"}})
		return
	}
	redirectBack(url.Values{"discord_linked": {"1"}})
}

type discordTestResponse struct {
	OK         bool   `json:"ok"`
	DurationMS int64  `json:"duration_ms"`
	Message    string `json:"message"`
}

// HandleAdminTest handles POST /admin/notifications/discord/test: verifies
// the configured bot token by fetching the bot's own identity.
func (h *DiscordNotificationsHandler) HandleAdminTest(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	botUser, err := h.system.TestDiscordBot(r.Context())
	duration := time.Since(start).Milliseconds()
	if errors.Is(err, notifications.ErrDiscordNotConfigured) {
		writeJSON(w, http.StatusOK, discordTestResponse{
			OK: false, DurationMS: duration, Message: "Bot token is not configured",
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusOK, discordTestResponse{
			OK: false, DurationMS: duration, Message: err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, discordTestResponse{
		OK: true, DurationMS: duration, Message: "Connected as " + botUser.Username,
	})
}
