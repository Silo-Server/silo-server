package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"time"

	silomail "github.com/Silo-Server/silo-server/internal/mail"
)

// EmailHandler exposes admin operations for the shared outbound email
// facility (internal/mail). Feature-specific email content lives with the
// features; this handler only owns configuration verification.
type EmailHandler struct {
	sender silomail.Sender
}

// NewEmailHandler creates an EmailHandler.
func NewEmailHandler(sender silomail.Sender) *EmailHandler {
	return &EmailHandler{sender: sender}
}

type emailTestRequest struct {
	To string `json:"to"`
}

type emailTestResponse struct {
	OK         bool   `json:"ok"`
	DurationMS int64  `json:"duration_ms"`
	Message    string `json:"message,omitempty"`
}

// HandleTest handles POST /admin/email/test: synchronously sends a test
// message so admins can verify SMTP settings before any feature depends on
// them.
func (h *EmailHandler) HandleTest(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.sender == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Email is not available")
		return
	}
	var req emailTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if _, err := mail.ParseAddress(req.To); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "A valid recipient address is required")
		return
	}

	duration, err := h.sendTestEmail(r.Context(), req.To)
	response := emailTestResponse{
		OK:         err == nil,
		DurationMS: duration,
	}
	switch {
	case err == nil:
	case errors.Is(err, silomail.ErrNotConfigured):
		response.Message = "Email is not configured. Set the SMTP host, from address, and enable email first."
	default:
		response.Message = err.Error()
	}
	writeJSON(w, http.StatusOK, response)
}

// sendTestEmail constructs and dispatches the same message for both transports.
func (h *EmailHandler) sendTestEmail(ctx context.Context, to string) (int64, error) {
	started := time.Now()
	err := h.sender.Send(ctx, silomail.Message{
		To:      []string{to},
		Subject: "Silo test email",
		TextBody: "This is a test email from your Silo server.\n\n" +
			"If you received it, outbound email is configured correctly.",
		HTMLBody: silomail.RenderLayout(silomail.LayoutOptions{
			Preheader: "Outbound email from your Silo server is configured correctly.",
			Title:     "Outbound email is working",
			BodyHTML: silomail.EmailParagraph("This is a test email from your Silo server.") +
				silomail.EmailParagraph("If you're reading it, the SMTP settings are correct and "+
					"notification emails will look like this one."),
		}),
	})
	return time.Since(started).Milliseconds(), err
}
