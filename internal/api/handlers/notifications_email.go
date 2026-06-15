package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

// emailPreferencesResponse is one profile's email notification state. The
// channel is profile-scoped: each profile verifies its own destination
// address and receives nothing until it has one — there is no account-email
// fallback.
type emailPreferencesResponse struct {
	Mode string `json:"mode"`
	// CustomEmail is the verified destination ('' = none; channel inert).
	CustomEmail string `json:"custom_email"`
	// PendingEmail is an address awaiting link-click verification.
	PendingEmail string `json:"pending_email"`
	// CanEditAddress is false for child profiles, which cannot set
	// addresses (and so cannot receive email notifications).
	CanEditAddress bool `json:"can_edit_address"`
}

type updateEmailPreferencesRequest struct {
	Mode string `json:"mode"`
}

type updateEmailAddressRequest struct {
	Email string `json:"email"`
}

func emailPreferencesPayload(state notifications.EmailPreferencesState) emailPreferencesResponse {
	return emailPreferencesResponse{
		Mode:           state.Mode,
		CustomEmail:    state.CustomEmail,
		PendingEmail:   state.PendingEmail,
		CanEditAddress: !state.IsChild,
	}
}

// respondEmailPreferences re-reads and writes the profile's full email state,
// so every mutation returns the same shape as GET.
func (h *NotificationsHandler) respondEmailPreferences(w http.ResponseWriter, r *http.Request, userID int, profileID string) {
	state, err := h.system.EmailPreferences(r.Context(), userID, profileID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load email preferences")
		return
	}
	writeJSON(w, http.StatusOK, emailPreferencesPayload(state))
}

// HandleGetEmailPreferences handles GET /notifications/email-preferences.
func (h *NotificationsHandler) HandleGetEmailPreferences(w http.ResponseWriter, r *http.Request) {
	h.respondEmailPreferences(w, r, apimw.GetUserID(r.Context()), apimw.GetProfileID(r.Context()))
}

// HandleUpdateEmailPreferences handles PUT /notifications/email-preferences.
func (h *NotificationsHandler) HandleUpdateEmailPreferences(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	var req updateEmailPreferencesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	err := h.system.SetEmailMode(r.Context(), userID, profileID, req.Mode)
	switch {
	case err == nil:
	case errors.Is(err, notifications.ErrEmailModeInvalid):
		writeError(w, http.StatusBadRequest, "bad_request", "Unknown email notification mode")
		return
	case errors.Is(err, notifications.ErrEmailModeNotAllowed):
		writeError(w, http.StatusBadRequest, "not_allowed", "Per-episode email is disabled by the administrator")
		return
	case errors.Is(err, notifications.ErrEmailNoAddress):
		writeError(w, http.StatusBadRequest, "no_email", "Verify an email address for this profile first")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save email preferences")
		return
	}
	h.respondEmailPreferences(w, r, userID, profileID)
}

// HandleRequestEmailAddress handles PUT /notifications/email-preferences/address.
// It stores the candidate address and emails it a verification link; the
// address only becomes the destination once that link is clicked.
func (h *NotificationsHandler) HandleRequestEmailAddress(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	var req updateEmailAddressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	err := h.system.RequestEmailAddress(r.Context(), userID, profileID, req.Email)
	switch {
	case err == nil:
	case errors.Is(err, notifications.ErrEmailInvalidAddress):
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid email address")
		return
	case errors.Is(err, notifications.ErrEmailChildProfile):
		writeError(w, http.StatusForbidden, "child_profile", "Child profiles cannot set a custom notification address")
		return
	case errors.Is(err, notifications.ErrEmailAddressInUse):
		writeError(w, http.StatusConflict, "address_in_use", "That email address is already used by another profile or account")
		return
	case errors.Is(err, notifications.ErrEmailVerifyRateLimited):
		writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many verification emails; try again later")
		return
	case errors.Is(err, notifications.ErrEmailNoLinkBase):
		writeError(w, http.StatusConflict, "no_external_url", "The server has no external URL configured for verification links")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to send the verification email")
		return
	}
	h.respondEmailPreferences(w, r, userID, profileID)
}

// HandleClearEmailAddress handles DELETE /notifications/email-preferences/address.
func (h *NotificationsHandler) HandleClearEmailAddress(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())

	err := h.system.ClearEmailAddress(r.Context(), userID, profileID)
	switch {
	case err == nil:
	case errors.Is(err, notifications.ErrEmailChildProfile):
		writeError(w, http.StatusForbidden, "child_profile", "Child profiles cannot change the notification address")
		return
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to remove the custom address")
		return
	}
	h.respondEmailPreferences(w, r, userID, profileID)
}

// EmailLinkHandler serves the public tokenized email endpoints: address
// verification and unsubscribe. Both are clicked from email clients on
// devices that may have no Silo session, so they render minimal standalone
// HTML instead of redirecting into the authenticated app.
type EmailLinkHandler struct {
	system *notifications.System
}

// NewEmailLinkHandler creates an EmailLinkHandler.
func NewEmailLinkHandler(system *notifications.System) *EmailLinkHandler {
	return &EmailLinkHandler{system: system}
}

// HandleVerify handles GET /notifications/email/verify?token=...
func (h *EmailLinkHandler) HandleVerify(w http.ResponseWriter, r *http.Request) {
	outcome, err := h.system.VerifyEmailToken(r.Context(), r.URL.Query().Get("token"))
	switch {
	case err != nil:
		writeEmailLinkPage(w, http.StatusInternalServerError, "Something went wrong",
			"The address could not be verified. Try the link again in a moment.")
	case outcome == notifications.EmailVerifyConflict:
		writeEmailLinkPage(w, http.StatusConflict, "Address already in use",
			"This address now belongs to another profile or account. Choose a different address in Silo's notification settings.")
	case outcome == notifications.EmailVerifyInvalid:
		writeEmailLinkPage(w, http.StatusBadRequest, "Link expired or already used",
			"Request a new verification email from Silo's notification settings.")
	default:
		writeEmailLinkPage(w, http.StatusOK, "Address verified",
			"Silo notifications for this profile will now be delivered here. You can close this page.")
	}
}

// HandleUnsubscribe handles GET and POST /notifications/email/unsubscribe?token=...
// POST is the RFC 8058 one-click target mail clients call directly.
func (h *EmailLinkHandler) HandleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	ok, err := h.system.UnsubscribeEmail(r.Context(), r.URL.Query().Get("token"))
	switch {
	case err != nil:
		writeEmailLinkPage(w, http.StatusInternalServerError, "Something went wrong",
			"Could not unsubscribe. Try the link again in a moment.")
	case !ok:
		writeEmailLinkPage(w, http.StatusBadRequest, "Link invalid",
			"This unsubscribe link is no longer valid. Manage notifications in Silo's settings.")
	default:
		writeEmailLinkPage(w, http.StatusOK, "Unsubscribed",
			"This profile will no longer receive notification emails. Re-enable them any time in Silo's settings.")
	}
}

// writeEmailLinkPage renders the minimal standalone page behind tokenized
// email links.
func writeEmailLinkPage(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s — Silo</title></head>
<body style="font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;background:#101014;color:#e8e8ec;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;">
<div style="max-width:420px;padding:32px;text-align:center;">
<h1 style="font-size:20px;margin:0 0 12px;">%s</h1>
<p style="font-size:14px;color:#9a9aa4;margin:0;">%s</p>
</div></body></html>`,
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(detail))
}
