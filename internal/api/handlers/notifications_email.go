package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

// emailPreferencesResponse is the account-level email notification setting.
// Unlike the per-profile preferences, one mode covers every profile on the
// login account: email addresses live on accounts, and the emails themselves
// aggregate across profiles.
type emailPreferencesResponse struct {
	Mode string `json:"mode"`
}

type updateEmailPreferencesRequest struct {
	Mode string `json:"mode"`
}

// HandleGetEmailPreferences handles GET /notifications/email-preferences.
func (h *NotificationsHandler) HandleGetEmailPreferences(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	mode, err := h.system.EmailMode(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load email preferences")
		return
	}
	writeJSON(w, http.StatusOK, emailPreferencesResponse{Mode: mode})
}

// HandleUpdateEmailPreferences handles PUT /notifications/email-preferences.
func (h *NotificationsHandler) HandleUpdateEmailPreferences(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())

	var req updateEmailPreferencesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	err := h.system.SetEmailMode(r.Context(), userID, req.Mode)
	switch {
	case err == nil:
	case errors.Is(err, notifications.ErrEmailModeInvalid):
		writeError(w, http.StatusBadRequest, "bad_request", "Unknown email notification mode")
		return
	case errors.Is(err, notifications.ErrEmailModeNotAllowed):
		writeError(w, http.StatusBadRequest, "not_allowed", "Per-episode email is disabled by the administrator")
		return
	case errors.Is(err, notifications.ErrEmailNoAddress):
		writeError(w, http.StatusBadRequest, "no_email", "Your account has no email address")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save email preferences")
		return
	}
	writeJSON(w, http.StatusOK, emailPreferencesResponse{Mode: req.Mode})
}
