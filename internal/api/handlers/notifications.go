package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/go-chi/chi/v5"
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

type notificationApplePushDisplayResponse = notifications.NotificationDisplay

type unreadCountResponse struct {
	Count int `json:"count"`
}

type wsTicketResponse struct {
	Ticket    string `json:"ticket"`
	ExpiresIn int    `json:"expires_in"`
}

func parseNotificationsLimit(r *http.Request, fallback int) int {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return fallback
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return fallback
	}
	return min(limit, notificationsMaxLimit)
}

// HandleList handles GET /notifications (newest-first inbox page).
func (h *NotificationsHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	profileID := apimw.GetProfileID(r.Context())
	unreadOnly := r.URL.Query().Get("status") == "unread"
	limit := parseNotificationsLimit(r, notificationsDefaultLimit)

	var before *notifications.Cursor
	if raw := r.URL.Query().Get("before"); raw != "" {
		cursor, err := notifications.DecodeCursor(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid before cursor")
			return
		}
		before = &cursor
	}

	rows, err := h.system.Deliveries.ListInbox(r.Context(), profileID, unreadOnly, limit, before)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list notifications")
		return
	}
	response := notificationListResponse{Notifications: h.system.PayloadsForRows(r.Context(), rows)}
	if len(rows) == limit {
		last := rows[len(rows)-1]
		response.NextCursor = notifications.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}.Encode()
	}
	writeJSON(w, http.StatusOK, response)
}

// HandleSync handles GET /notifications/sync — the forward (ascending) cursor
// sync used by clients waking from a push or reconnecting after a gap.
func (h *NotificationsHandler) HandleSync(w http.ResponseWriter, r *http.Request) {
	profileID := apimw.GetProfileID(r.Context())
	limit := parseNotificationsLimit(r, notificationsSyncLimit)

	var since *notifications.Cursor
	if raw := r.URL.Query().Get("since"); raw != "" {
		cursor, err := notifications.DecodeCursor(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", "Invalid since cursor")
			return
		}
		since = &cursor
	}

	rows, err := h.system.Deliveries.ListSync(r.Context(), profileID, since, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to sync notifications")
		return
	}
	unread, err := h.system.Deliveries.UnreadCount(r.Context(), profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to count unread notifications")
		return
	}

	response := notificationSyncResponse{
		Notifications: h.system.PayloadsForRows(r.Context(), rows),
		UnreadCount:   unread,
	}
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		response.NextCursor = notifications.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}.Encode()
	} else if since != nil {
		response.NextCursor = since.Encode()
	}
	writeJSON(w, http.StatusOK, response)
}

// HandleGet handles GET /notifications/{id}; 404 for other profiles' rows.
func (h *NotificationsHandler) HandleGet(w http.ResponseWriter, r *http.Request) {
	profileID := apimw.GetProfileID(r.Context())
	id := chi.URLParam(r, "id")

	row, err := h.system.Deliveries.GetByID(r.Context(), profileID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load notification")
		return
	}
	if row == nil {
		writeError(w, http.StatusNotFound, "not_found", "Notification not found")
		return
	}
	writeJSON(w, http.StatusOK, h.system.PayloadForRow(r.Context(), *row))
}

// HandleApplePushDisplay handles GET /notifications/push/apple/display/{delivery_id}.
// It returns only compact display metadata for notification-service-extension
// enrichment, scoped to the active profile.
func (h *NotificationsHandler) HandleApplePushDisplay(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	profileID := apimw.GetProfileID(r.Context())
	id := chi.URLParam(r, "delivery_id")
	if id == "" {
		writeError(w, http.StatusNotFound, "not_found", "Notification not found")
		return
	}
	row, err := h.system.Deliveries.GetByID(r.Context(), profileID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load notification")
		return
	}
	if row == nil {
		writeError(w, http.StatusNotFound, "not_found", "Notification not found")
		return
	}
	writeJSON(w, http.StatusOK, notificationApplePushDisplayResponse(notifications.BuildNotificationDisplay(*row)))
}

// HandleUnreadCount handles GET /notifications/unread-count.
func (h *NotificationsHandler) HandleUnreadCount(w http.ResponseWriter, r *http.Request) {
	profileID := apimw.GetProfileID(r.Context())
	count, err := h.system.Deliveries.UnreadCount(r.Context(), profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to count unread notifications")
		return
	}
	writeJSON(w, http.StatusOK, unreadCountResponse{Count: count})
}

// HandleMarkRead handles POST /notifications/{id}/read. Idempotent.
func (h *NotificationsHandler) HandleMarkRead(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	id := chi.URLParam(r, "id")

	transitioned, err := h.system.Deliveries.MarkRead(r.Context(), profileID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to mark notification read")
		return
	}
	if !transitioned {
		// Already read is fine (idempotent); unknown IDs are a 404.
		exists, err := h.system.Deliveries.Exists(r.Context(), profileID, id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to mark notification read")
			return
		}
		if !exists {
			writeError(w, http.StatusNotFound, "not_found", "Notification not found")
			return
		}
	}
	if transitioned {
		h.publishReadEvent(r, userID, profileID, id)
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleReadAll handles POST /notifications/read-all.
func (h *NotificationsHandler) HandleReadAll(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	if _, err := h.system.Deliveries.MarkAllRead(r.Context(), profileID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to mark notifications read")
		return
	}
	h.publishReadEvent(r, userID, profileID, "")
	w.WriteHeader(http.StatusNoContent)
}

// publishReadEvent lets other connected tabs of the same profile reconcile
// read state. An empty id means "all read".
func (h *NotificationsHandler) publishReadEvent(r *http.Request, userID int, profileID, id string) {
	if h.hub == nil {
		return
	}
	payload := map[string]any{"profile_id": profileID}
	if id != "" {
		payload["id"] = id
	} else {
		payload["all"] = true
	}
	_ = h.hub.PublishJSON(r.Context(), evt.ChannelNotifications, notifications.EventNotificationRead,
		payload, evt.PublishOptions{UserID: userID, ProfileID: profileID})
}

// HandleGetPreferences handles GET /notifications/preferences.
func (h *NotificationsHandler) HandleGetPreferences(w http.ResponseWriter, r *http.Request) {
	profileID := apimw.GetProfileID(r.Context())
	prefs, err := h.system.Preferences.Get(r.Context(), profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load notification preferences")
		return
	}
	writeJSON(w, http.StatusOK, prefs)
}

type updatePreferencesRequest struct {
	Enabled                *bool `json:"enabled"`
	NotifyFavorites        *bool `json:"notify_favorites"`
	NotifyWatchlist        *bool `json:"notify_watchlist"`
	NotifyContinueWatching *bool `json:"notify_continue_watching"`
	NotifyNextUp           *bool `json:"notify_next_up"`
}

// HandleUpdatePreferences handles PUT /notifications/preferences. Fields are
// optional; omitted fields keep their current value. Idempotent.
func (h *NotificationsHandler) HandleUpdatePreferences(w http.ResponseWriter, r *http.Request) {
	profileID := apimw.GetProfileID(r.Context())

	var req updatePreferencesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	prefs, err := h.system.Preferences.Get(r.Context(), profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load notification preferences")
		return
	}
	if req.Enabled != nil {
		prefs.Enabled = *req.Enabled
	}
	if req.NotifyFavorites != nil {
		prefs.NotifyFavorites = *req.NotifyFavorites
	}
	if req.NotifyWatchlist != nil {
		prefs.NotifyWatchlist = *req.NotifyWatchlist
	}
	if req.NotifyContinueWatching != nil {
		prefs.NotifyContinueWatching = *req.NotifyContinueWatching
	}
	if req.NotifyNextUp != nil {
		prefs.NotifyNextUp = *req.NotifyNextUp
	}
	if err := h.system.Preferences.Upsert(r.Context(), prefs); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save notification preferences")
		return
	}
	writeJSON(w, http.StatusOK, prefs)
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
}

type capabilityWebhooks struct {
	Available      bool     `json:"available"`
	MaxPerProfile  int      `json:"max_per_profile"`
	SupportedTypes []string `json:"supported_types"`
}

// HandleCapability handles GET /notifications/capability. Clients render
// setup UI from this response instead of introspecting admin settings.
func (h *NotificationsHandler) HandleCapability(w http.ResponseWriter, r *http.Request) {
	webhooks := capabilityWebhooks{Available: false, MaxPerProfile: 0, SupportedTypes: []string{}}
	if h.system.Webhooks != nil && h.system.Settings.WebhooksEnabled(r.Context()) {
		webhooks = capabilityWebhooks{
			Available:      true,
			MaxPerProfile:  h.system.Settings.WebhooksMaxPerProfile(r.Context()),
			SupportedTypes: []string{"discord", "generic"},
		}
	}
	webPush := capabilityWebPush{}
	if h.system.WebPush != nil && h.system.Settings.WebPushEnabled(r.Context()) {
		if publicKey, err := h.system.WebPush.PublicKey(r.Context()); err == nil && publicKey != "" {
			webPush = capabilityWebPush{Available: true, PublicKey: publicKey}
		}
	}
	email := capabilityAccountChannel{Modes: []string{}}
	if h.system.EmailAvailable(r.Context()) {
		modes := []string{notifications.ChannelModeDailyDigest}
		if h.system.Settings.EmailAllowPerEpisode(r.Context()) {
			modes = append(modes,
				notifications.ChannelModePerEpisode,
				notifications.ChannelModePerEpisodeAndDigest)
		}
		email = capabilityAccountChannel{
			Available:  true,
			Modes:      modes,
			DigestHour: h.system.Settings.EmailDigestHour(r.Context()),
		}
	}
	discordCap := capabilityAccountChannel{Modes: []string{}}
	if h.system.DiscordAvailable(r.Context()) {
		modes := []string{notifications.ChannelModeDailyDigest}
		if h.system.Settings.DiscordAllowPerEpisode(r.Context()) {
			modes = append(modes,
				notifications.ChannelModePerEpisode,
				notifications.ChannelModePerEpisodeAndDigest)
		}
		discordCap = capabilityAccountChannel{
			Available:  true,
			Modes:      modes,
			DigestHour: h.system.Settings.DiscordDigestHour(r.Context()),
		}
	}
	applePush := capabilityPush{Available: false, Provider: "off", SupportedModes: []string{"in_app_only"}}
	androidPush := capabilityPush{Available: false, Provider: "off", SupportedModes: []string{"in_app_only"}}
	// Like web push above, availability requires both the wiring (cipher/store)
	// and the admin delivery toggle: Available must mean "setup will actually
	// deliver", not "the server could store a token".
	if h.system.PushDevices != nil && h.system.PushDevices.Available() {
		if h.system.Settings.ApplePushDeliveryEnabled(r.Context()) {
			applePush = capabilityPush{
				Available:      true,
				Provider:       notifications.PushProviderSiloRelay,
				SupportedModes: []string{notifications.PushModePrivatePush, notifications.PushModeInAppOnly},
			}
		}
		if h.system.Settings.AndroidPushDeliveryEnabled(r.Context()) {
			androidPush = capabilityPush{
				Available:      true,
				Provider:       notifications.PushProviderSiloRelay,
				SupportedModes: []string{notifications.PushModePrivatePush, notifications.PushModeInAppOnly},
			}
		}
	}
	writeJSON(w, http.StatusOK, capabilityResponse{
		InApp:       capabilityInApp{Enabled: h.system.Settings.UIEnabled(r.Context())},
		ApplePush:   applePush,
		AndroidPush: androidPush,
		WebPush:     webPush,
		Webhooks:    webhooks,
		Email:       email,
		Discord:     discordCap,
	})
}

// HandleMintWSTicket handles POST /events/ws-ticket: mints a short-lived
// single-use websocket handshake ticket bound to (user, profile). Long-lived
// tokens must never ride the websocket query string — reverse-proxy access
// logs capture it.
func (h *NotificationsHandler) HandleMintWSTicket(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	ticket, ttl, err := h.system.Tickets.Mint(r.Context(), userID, profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to mint websocket ticket")
		return
	}
	writeJSON(w, http.StatusOK, wsTicketResponse{
		Ticket:    ticket,
		ExpiresIn: int(ttl.Seconds()),
	})
}
