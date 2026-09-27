package handlers

import (
	"context"
	"errors"
	"net/mail"

	silomail "github.com/Silo-Server/silo-server/internal/mail"
)

var ErrAdminEmailUnavailable = errors.New("administrator email is unavailable")
var ErrAdminEmailRecipient = errors.New("invalid test email recipient")

type AdminEmailTestResult struct {
	OK         bool
	DurationMS int64
	Message    string
}

// SendAdminTestEmail performs one synchronous send. Provider error text never
// becomes v2 response data: SMTP failures can contain credentials or messages.
func (h *EmailHandler) SendAdminTestEmail(ctx context.Context, to string) (AdminEmailTestResult, error) {
	if h == nil || h.sender == nil {
		return AdminEmailTestResult{}, ErrAdminEmailUnavailable
	}
	if _, err := mail.ParseAddress(to); err != nil {
		return AdminEmailTestResult{}, ErrAdminEmailRecipient
	}
	duration, err := h.sendTestEmail(ctx, to)
	result := AdminEmailTestResult{OK: err == nil, DurationMS: duration}
	switch {
	case err == nil:
	case errors.Is(err, silomail.ErrNotConfigured):
		result.Message = "Email is not configured. Set the SMTP host, from address, and enable email first."
	default:
		result.Message = "The mail server did not confirm delivery. Check the saved email settings before sending another test."
	}
	return result, nil
}
