package handlers

import (
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

	started := time.Now()
	err := h.sender.Send(r.Context(), silomail.Message{
		To:      []string{req.To},
		Subject: "Silo test email",
		TextBody: "This is a test email from your Silo server.\n\n" +
			"If you received it, outbound email is configured correctly.",
	})
	response := emailTestResponse{
		OK:         err == nil,
		DurationMS: time.Since(started).Milliseconds(),
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
