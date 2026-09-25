package passwordreset

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/mail"
)

// emailContent is one rendered reset email.
type emailContent struct {
	Subject string
	Text    string
	HTML    string
}

// composeResetEmail renders the reset message. username is escaped anyway;
// resetURL is server-built.
func composeResetEmail(username, serverName, resetURL string, expiresAt, now time.Time) emailContent {
	product := strings.TrimSpace(serverName)
	if product == "" {
		product = "Silo"
	}
	expiry := mail.ExpiryPhrase(expiresAt, now)
	fine := fmt.Sprintf(
		"This link works once and expires %s. Using it signs you out on every device. "+
			"If you didn't ask for a new password, ignore this email — your password stays the same.", expiry)

	var text strings.Builder
	fmt.Fprintf(&text, "An admin sent you a link to choose a new password for your %s account.\n\n", product)
	fmt.Fprintf(&text, "To choose a new password, open this link:\n\n  %s\n\n", resetURL)
	fmt.Fprintf(&text, "Account: %s\n\n%s\n", username, fine)

	var body strings.Builder
	body.WriteString(mail.EmailParagraph(
		fmt.Sprintf("An admin sent you a link to choose a new password for your %s account.", product)))
	body.WriteString(mail.EmailButton("Choose a new password", resetURL))
	body.WriteString(mail.EmailFacts(
		mail.EmailFact{Label: "Account", ValueHTML: html.EscapeString(username), Mono: true},
		mail.EmailFact{Label: "Link expires", ValueHTML: html.EscapeString(expiry)},
	))
	body.WriteString(mail.EmailLinkFallback(resetURL))

	return emailContent{
		Subject: fmt.Sprintf("Choose a new password for %s", product),
		Text:    text.String(),
		HTML: mail.RenderLayout(mail.LayoutOptions{
			Preheader:  fmt.Sprintf("Choose a new password — the link expires %s.", expiry),
			Title:      "Reset your password",
			BodyHTML:   body.String(),
			FooterHTML: html.EscapeString(fine),
		}),
	}
}
