package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/go-chi/chi/v5"
)

// AdminServerChannelsHandler exposes admin CRUD for server notification
// channels (community broadcast destinations). All routes are mounted inside
// the admin-only group.
type AdminServerChannelsHandler struct {
	system *notifications.System
}

// NewAdminServerChannelsHandler creates the handler.
func NewAdminServerChannelsHandler(system *notifications.System) *AdminServerChannelsHandler {
	return &AdminServerChannelsHandler{system: system}
}

func (h *AdminServerChannelsHandler) service() *notifications.ServerChannelService {
	if h == nil || h.system == nil {
		return nil
	}
	return h.system.ServerChannels
}

// serverChannelResponse is the API view of a server channel. Like webhooks it
// never includes the destination URL (Discord webhook tokens are bearer
// credentials in the URL path) or the stored signing secret — only url_host.
type serverChannelResponse struct {
	ID                     string     `json:"id"`
	Name                   string     `json:"name"`
	Type                   string     `json:"type"`
	URLHost                string     `json:"url_host"`
	Enabled                bool       `json:"enabled"`
	NotifyNewMovies        bool       `json:"notify_new_movies"`
	NotifyNewEpisodes      bool       `json:"notify_new_episodes"`
	NotifyRequestSubmitted bool       `json:"notify_request_submitted"`
	NotifyRequestApproved  bool       `json:"notify_request_approved"`
	NotifyRequestDeclined  bool       `json:"notify_request_declined"`
	NotifyRequestFulfilled bool       `json:"notify_request_fulfilled"`
	ConsecutiveFailures    int        `json:"consecutive_failures"`
	DisabledReason         *string    `json:"disabled_reason"`
	LastSuccessAt          *time.Time `json:"last_success_at"`
	LastFailureAt          *time.Time `json:"last_failure_at"`
	LastFailureStatus      *int       `json:"last_failure_status"`
	LastFailureMessage     *string    `json:"last_failure_message"`
	CreatedAt              time.Time  `json:"created_at"`
	// SigningSecret is present only in create / rotate-secret responses.
	SigningSecret string `json:"signing_secret,omitempty"`
}

func serverChannelToResponse(ch notifications.ServerChannel) serverChannelResponse {
	return serverChannelResponse{
		ID:                     ch.ID,
		Name:                   ch.Name,
		Type:                   ch.Type,
		URLHost:                ch.URLHost,
		Enabled:                ch.Enabled,
		NotifyNewMovies:        ch.NotifyNewMovies,
		NotifyNewEpisodes:      ch.NotifyNewEpisodes,
		NotifyRequestSubmitted: ch.NotifyRequestSubmitted,
		NotifyRequestApproved:  ch.NotifyRequestApproved,
		NotifyRequestDeclined:  ch.NotifyRequestDeclined,
		NotifyRequestFulfilled: ch.NotifyRequestFulfilled,
		ConsecutiveFailures:    ch.ConsecutiveFailures,
		DisabledReason:         ch.DisabledReason,
		LastSuccessAt:          ch.LastSuccessAt,
		LastFailureAt:          ch.LastFailureAt,
		LastFailureStatus:      ch.LastFailureStatus,
		LastFailureMessage:     ch.LastFailureMessage,
		CreatedAt:              ch.CreatedAt,
	}
}

type serverChannelRequest struct {
	Name                   *string `json:"name"`
	URL                    *string `json:"url"`
	Type                   *string `json:"type"`
	Enabled                *bool   `json:"enabled"`
	NotifyNewMovies        *bool   `json:"notify_new_movies"`
	NotifyNewEpisodes      *bool   `json:"notify_new_episodes"`
	NotifyRequestSubmitted *bool   `json:"notify_request_submitted"`
	NotifyRequestApproved  *bool   `json:"notify_request_approved"`
	NotifyRequestDeclined  *bool   `json:"notify_request_declined"`
	NotifyRequestFulfilled *bool   `json:"notify_request_fulfilled"`
}

func (r serverChannelRequest) toInput() notifications.ServerChannelInput {
	return notifications.ServerChannelInput{
		Name:                   r.Name,
		URL:                    r.URL,
		Type:                   r.Type,
		Enabled:                r.Enabled,
		NotifyNewMovies:        r.NotifyNewMovies,
		NotifyNewEpisodes:      r.NotifyNewEpisodes,
		NotifyRequestSubmitted: r.NotifyRequestSubmitted,
		NotifyRequestApproved:  r.NotifyRequestApproved,
		NotifyRequestDeclined:  r.NotifyRequestDeclined,
		NotifyRequestFulfilled: r.NotifyRequestFulfilled,
	}
}

func writeServerChannelError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, notifications.ErrServerChannelsDisabled):
		writeError(w, http.StatusForbidden, "server_channels_disabled", "Server channels are disabled")
	case errors.Is(err, notifications.ErrServerChannelNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Server channel not found")
	case errors.Is(err, notifications.ErrServerChannelLimit):
		writeError(w, http.StatusUnprocessableEntity, "limit_reached", "Server channel limit reached")
	case errors.Is(err, notifications.ErrServerChannelInvalid):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Server channel operation failed")
	}
}

// HandleList handles GET /admin/notifications/server-channels.
func (h *AdminServerChannelsHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	service := h.service()
	if service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Server channels are not available")
		return
	}
	channels, err := service.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list server channels")
		return
	}
	responses := make([]serverChannelResponse, 0, len(channels))
	for _, ch := range channels {
		responses = append(responses, serverChannelToResponse(ch))
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": responses})
}

// HandleCreate handles POST /admin/notifications/server-channels. For generic
// channels the response carries the signing secret exactly once.
func (h *AdminServerChannelsHandler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	service := h.service()
	if service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Server channels are not available")
		return
	}
	var req serverChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	ch, signingSecret, err := service.Create(r.Context(), apimw.GetUserID(r.Context()), req.toInput())
	if err != nil {
		writeServerChannelError(w, err)
		return
	}
	response := serverChannelToResponse(*ch)
	response.SigningSecret = signingSecret
	writeJSON(w, http.StatusCreated, response)
}

// HandleUpdate handles PUT /admin/notifications/server-channels/{id}.
func (h *AdminServerChannelsHandler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	service := h.service()
	if service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Server channels are not available")
		return
	}
	var req serverChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	ch, err := service.Update(r.Context(), chi.URLParam(r, "id"), req.toInput())
	if err != nil {
		writeServerChannelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, serverChannelToResponse(*ch))
}

// HandleDelete handles DELETE /admin/notifications/server-channels/{id}.
// Idempotent.
func (h *AdminServerChannelsHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	service := h.service()
	if service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Server channels are not available")
		return
	}
	if err := service.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeServerChannelError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleRotateSecret handles POST /admin/notifications/server-channels/{id}/rotate-secret.
func (h *AdminServerChannelsHandler) HandleRotateSecret(w http.ResponseWriter, r *http.Request) {
	service := h.service()
	if service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Server channels are not available")
		return
	}
	signingSecret, err := service.RotateSecret(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeServerChannelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"signing_secret": signingSecret})
}

// HandleTest handles POST /admin/notifications/server-channels/{id}/test. The
// test send is synchronous and never touches the watermark or failure
// counters.
func (h *AdminServerChannelsHandler) HandleTest(w http.ResponseWriter, r *http.Request) {
	service := h.service()
	if service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Server channels are not available")
		return
	}
	result, err := service.Test(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeServerChannelError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
