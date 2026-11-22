package handlers

import (
	"context"
	"fmt"
	"html"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

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
// Only POST changes preferences. It is both the confirmation page's button and
// the RFC 8058 one-click target, so it needs no login and accepts no-referrer
// form posts with Origin: null. Any other request only renders the
// confirmation, so a link scanner or prefetch can't unsubscribe anyone.
func (h *EmailLinkHandler) HandleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if r.Method != http.MethodPost {
		if token == "" {
			writeEmailLinkPage(w, http.StatusBadRequest, "Link invalid",
				"This unsubscribe link is no longer valid. Manage notifications in Silo's settings.")
			return
		}
		writeEmailLinkPageWithConfirmation(w, http.StatusOK, "Unsubscribe from notification emails?",
			"Choose Unsubscribe to stop notification emails for this profile. Opening this page has not changed your preferences.", true)
		return
	}
	ok, err := h.system.UnsubscribeEmail(r.Context(), token)
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
	writeEmailLinkPageWithConfirmation(w, status, title, detail, false)
}

func writeEmailLinkPageWithConfirmation(w http.ResponseWriter, status int, title, detail string, confirm bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s — Silo</title>
<style>button{font:inherit;font-weight:600;background:#e8e8ec;color:#101014;border:0;border-radius:6px;padding:12px 20px;cursor:pointer}button:focus-visible{outline:3px solid #e8e8ec;outline-offset:4px}</style></head>
<body style="font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;background:#101014;color:#e8e8ec;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;">
<main style="max-width:420px;padding:32px;text-align:center;">
<h1 style="font-size:20px;margin:0 0 12px;">%s</h1>
<p style="font-size:14px;color:#9a9aa4;margin:0;">%s</p>
`,
		html.EscapeString(title), html.EscapeString(title), html.EscapeString(detail))
	if confirm {
		// No action: POST back to this URL, preserving its token query and proxy
		// prefix without reflecting either into the page.
		_, _ = fmt.Fprint(w, `<form method="post" style="margin-top:24px;"><button type="submit">Unsubscribe</button></form>`)
	}
	_, _ = fmt.Fprint(w, `</main></body></html>`)
}
