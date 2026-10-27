package handlers

import (
	"context"
	"time"

	silomail "github.com/Silo-Server/silo-server/internal/mail"
)

// EmailHandler exposes admin operations for the shared outbound email
// facility (internal/mail). Feature-specific email content lives with the
// features; this handler only owns configuration verification.
type EmailHandler struct {
	sender silomail.Sender
	brand  *silomail.BrandLoader
}

// NewEmailHandler creates an EmailHandler. brand styles the test message so
// it previews the server's branded emails; nil sends Silo's default branding.
func NewEmailHandler(sender silomail.Sender, brand *silomail.BrandLoader) *EmailHandler {
	return &EmailHandler{sender: sender, brand: brand}
}

type emailTestRequest struct {
	To string `json:"to"`
}

type emailTestResponse struct {
	OK         bool   `json:"ok"`
	DurationMS int64  `json:"duration_ms"`
	Message    string `json:"message,omitempty"`
}

// sendTestEmail constructs and dispatches the same message for both transports.
func (h *EmailHandler) sendTestEmail(ctx context.Context, to string) (int64, error) {
	started := time.Now()
	brand := h.brand.Load(ctx)
	err := h.sender.Send(ctx, silomail.Message{
		To:      []string{to},
		Subject: "Silo test email",
		TextBody: "This is a test email from your Silo server.\n\n" +
			"If you received it, outbound email is configured correctly.",
		HTMLBody: silomail.RenderLayout(silomail.LayoutOptions{
			Brand:     brand,
			Preheader: "Outbound email from your Silo server is configured correctly.",
			Title:     "Outbound email is working",
			BodyHTML: silomail.EmailParagraph("This is a test email from your Silo server.") +
				silomail.EmailParagraph("If you're reading it, the SMTP settings are correct and "+
					"notification emails will look like this one."),
		}),
		Inline: brand.InlineImages(),
	})
	return time.Since(started).Milliseconds(), err
}
