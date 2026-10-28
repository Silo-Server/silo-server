package handlers

import (
	"context"
	"fmt"
	"html"
	"net/http"

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

// EmailLinkHandler serves the public tokenized email endpoints: address
// verification and unsubscribe. Both are clicked from email clients on
// devices that may have no Silo session, so they render minimal standalone
// HTML instead of redirecting into the authenticated app.
type NotificationEmailLinkProcessor interface {
	VerifyEmailToken(context.Context, string) (notifications.EmailVerifyOutcome, error)
	UnsubscribeEmail(context.Context, string) (bool, error)
}

type EmailLinkHandler struct {
	system NotificationEmailLinkProcessor
}

// NewEmailLinkHandler creates an EmailLinkHandler.
func NewEmailLinkHandler(system NotificationEmailLinkProcessor) *EmailLinkHandler {
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
