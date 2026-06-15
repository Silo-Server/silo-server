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

// webPushSubscriptionResponse is the API view of a browser push
// registration. The keys are write-only: clients re-subscribe rather than
// read them back.
type webPushSubscriptionResponse struct {
	ID            string     `json:"id"`
	Endpoint      string     `json:"endpoint"`
	DeviceName    string     `json:"device_name,omitempty"`
	Enabled       bool       `json:"enabled"`
	CreatedAt     time.Time  `json:"created_at"`
	LastSuccessAt *time.Time `json:"last_success_at"`
	LastFailureAt *time.Time `json:"last_failure_at"`
}

func webPushToResponse(sub notifications.WebPushSubscription) webPushSubscriptionResponse {
	return webPushSubscriptionResponse{
		ID:            sub.ID,
		Endpoint:      sub.Endpoint,
		DeviceName:    sub.DeviceName,
		Enabled:       sub.Enabled,
		CreatedAt:     sub.CreatedAt,
		LastSuccessAt: sub.LastSuccessAt,
		LastFailureAt: sub.LastFailureAt,
	}
}

func (h *NotificationsHandler) webPush() *notifications.WebPushService {
	if h == nil || h.system == nil {
		return nil
	}
	return h.system.WebPush
}

type webPushSubscribeRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	DeviceName string `json:"device_name"`
}

// HandleWebPushSubscribe handles POST /notifications/web-push/subscriptions.
// The body matches PushSubscription.toJSON() plus an optional device name.
func (h *NotificationsHandler) HandleWebPushSubscribe(w http.ResponseWriter, r *http.Request) {
	service := h.webPush()
	if service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Web push is not available")
		return
	}
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	var req webPushSubscribeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	sub, err := service.Subscribe(r.Context(), userID, profileID,
		req.Endpoint, req.Keys.P256dh, req.Keys.Auth, req.DeviceName)
	if err != nil {
		if errors.Is(err, notifications.ErrWebPushInvalid) {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to register push subscription")
		return
	}
	writeJSON(w, http.StatusCreated, webPushToResponse(*sub))
}

// HandleWebPushList handles GET /notifications/web-push/subscriptions.
func (h *NotificationsHandler) HandleWebPushList(w http.ResponseWriter, r *http.Request) {
	service := h.webPush()
	if service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Web push is not available")
		return
	}
	profileID := apimw.GetProfileID(r.Context())
	subs, err := service.List(r.Context(), profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list push subscriptions")
		return
	}
	responses := make([]webPushSubscriptionResponse, 0, len(subs))
	for _, sub := range subs {
		responses = append(responses, webPushToResponse(sub))
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": responses})
}

// HandleWebPushDelete handles DELETE /notifications/web-push/subscriptions/{id}.
func (h *NotificationsHandler) HandleWebPushDelete(w http.ResponseWriter, r *http.Request) {
	service := h.webPush()
	if service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Web push is not available")
		return
	}
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if err := service.Unsubscribe(r.Context(), userID, profileID, chi.URLParam(r, "id"), ""); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to remove push subscription")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type webPushUnsubscribeRequest struct {
	Endpoint string `json:"endpoint"`
}

// HandleWebPushUnsubscribe handles POST /notifications/web-push/unsubscribe.
// Browsers only know their endpoint, not the server-side row ID. Idempotent.
func (h *NotificationsHandler) HandleWebPushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	service := h.webPush()
	if service == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Web push is not available")
		return
	}
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	var req webPushUnsubscribeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Endpoint == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "An endpoint is required")
		return
	}
	if err := service.Unsubscribe(r.Context(), userID, profileID, "", req.Endpoint); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to remove push subscription")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
