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

func webhookToResponse(hook notifications.Webhook) webhookResponse {
	return webhookResponse{
		ID:                     hook.ID,
		Name:                   hook.Name,
		Type:                   hook.Type,
		URLHost:                hook.URLHost,
		Enabled:                hook.Enabled,
		NotifyFavorites:        hook.NotifyFavorites,
		NotifyWatchlist:        hook.NotifyWatchlist,
		NotifyContinueWatching: hook.NotifyContinueWatching,
		NotifyNextUp:           hook.NotifyNextUp,
		NotifyRequests:         hook.NotifyRequests,
		ConsecutiveFailures:    hook.ConsecutiveFailures,
		DisabledReason:         hook.DisabledReason,
		LastSuccessAt:          hook.LastSuccessAt,
		LastFailureAt:          hook.LastFailureAt,
		LastFailureStatus:      hook.LastFailureStatus,
		LastFailureMessage:     hook.LastFailureMessage,
	}
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

func (r webhookRequest) toInput() notifications.WebhookInput {
	return notifications.WebhookInput{
		Name:                   r.Name,
		URL:                    r.URL,
		Type:                   r.Type,
		Enabled:                r.Enabled,
		NotifyFavorites:        r.NotifyFavorites,
		NotifyWatchlist:        r.NotifyWatchlist,
		NotifyContinueWatching: r.NotifyContinueWatching,
		NotifyNextUp:           r.NotifyNextUp,
		NotifyRequests:         r.NotifyRequests,
	}
}

func (h *NotificationsHandler) webhooks() *notifications.WebhookService {
	if h == nil || h.system == nil {
		return nil
	}
	return h.system.Webhooks
}

func writeWebhookError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, notifications.ErrWebhooksDisabled):
		writeError(w, http.StatusForbidden, "webhooks_disabled", "Webhooks are disabled by the server administrator")
	case errors.Is(err, notifications.ErrWebhookNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Webhook not found")
	case errors.Is(err, notifications.ErrWebhookLimit):
		writeError(w, http.StatusUnprocessableEntity, "limit_reached", "Webhook limit reached for this profile")
	case errors.Is(err, notifications.ErrWebhookInvalid):
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Webhook operation failed")
	}
}

// HandleListWebhooks handles GET /notifications/webhooks.
func (h *NotificationsHandler) HandleListWebhooks(w http.ResponseWriter, r *http.Request) {
	service := h.webhooks()
	if service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Webhooks are not available")
		return
	}
	profileID := apimw.GetProfileID(r.Context())
	hooks, err := service.List(r.Context(), profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list webhooks")
		return
	}
	responses := make([]webhookResponse, 0, len(hooks))
	for _, hook := range hooks {
		responses = append(responses, webhookToResponse(hook))
	}
	writeJSON(w, http.StatusOK, map[string]any{"webhooks": responses})
}

// HandleCreateWebhook handles POST /notifications/webhooks. For generic
// webhooks the response carries the signing secret exactly once.
func (h *NotificationsHandler) HandleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	service := h.webhooks()
	if service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Webhooks are not available")
		return
	}
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	var req webhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	hook, signingSecret, err := service.Create(r.Context(), userID, profileID, req.toInput())
	if err != nil {
		writeWebhookError(w, err)
		return
	}
	response := webhookToResponse(*hook)
	response.SigningSecret = signingSecret
	writeJSON(w, http.StatusCreated, response)
}

// HandleUpdateWebhook handles PUT /notifications/webhooks/{id}.
func (h *NotificationsHandler) HandleUpdateWebhook(w http.ResponseWriter, r *http.Request) {
	service := h.webhooks()
	if service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Webhooks are not available")
		return
	}
	profileID := apimw.GetProfileID(r.Context())

	var req webhookRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	hook, err := service.Update(r.Context(), profileID, chi.URLParam(r, "id"), req.toInput())
	if err != nil {
		writeWebhookError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, webhookToResponse(*hook))
}

// HandleDeleteWebhook handles DELETE /notifications/webhooks/{id}. Idempotent.
func (h *NotificationsHandler) HandleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	service := h.webhooks()
	if service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Webhooks are not available")
		return
	}
	profileID := apimw.GetProfileID(r.Context())
	if err := service.Delete(r.Context(), profileID, chi.URLParam(r, "id")); err != nil {
		writeWebhookError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleRotateWebhookSecret handles POST /notifications/webhooks/{id}/rotate-secret.
func (h *NotificationsHandler) HandleRotateWebhookSecret(w http.ResponseWriter, r *http.Request) {
	service := h.webhooks()
	if service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Webhooks are not available")
		return
	}
	profileID := apimw.GetProfileID(r.Context())
	signingSecret, err := service.RotateSecret(r.Context(), profileID, chi.URLParam(r, "id"))
	if err != nil {
		writeWebhookError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"signing_secret": signingSecret})
}

// HandleTestWebhook handles POST /notifications/webhooks/{id}/test. The test
// send is synchronous and never touches the retry/auto-disable counters.
func (h *NotificationsHandler) HandleTestWebhook(w http.ResponseWriter, r *http.Request) {
	service := h.webhooks()
	if service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Webhooks are not available")
		return
	}
	profileID := apimw.GetProfileID(r.Context())
	result, err := service.Test(r.Context(), profileID, chi.URLParam(r, "id"))
	if err != nil {
		writeWebhookError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
