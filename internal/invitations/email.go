package invitations

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/mail"
)

// emailContent is one rendered invitation email.
type emailContent struct {
	Subject string
	Text    string
	HTML    string
}

// composeInvitationEmail renders the invitation message. inviterName and
// note are admin-controlled but escaped anyway; claimURL is server-built.
func composeInvitationEmail(inviterName, serverName, email, claimURL, note string, expiresAt time.Time, now time.Time) emailContent {
	inviter := strings.TrimSpace(inviterName)
	if inviter == "" {
		inviter = "An admin"
	}
	product := strings.TrimSpace(serverName)
	if product == "" {
		product = "Silo"
	}
	expiry := mail.ExpiryPhrase(expiresAt, now)

	fine := fmt.Sprintf(
		"This link works once and expires %s. If you weren't expecting this, "+
			"ignore it — no account is created until you use the link.", expiry)

	var text strings.Builder
	fmt.Fprintf(&text, "%s set up an account for you on %s.\n\n", inviter, product)
	if note != "" {
		fmt.Fprintf(&text, "%q\n\n", note)
	}
	fmt.Fprintf(&text, "To choose a password and get started, open this link:\n\n  %s\n\n", claimURL)
	fmt.Fprintf(&text, "You'll sign in with this email address: %s\n\n%s\n", email, fine)

	var body strings.Builder
	body.WriteString(mail.EmailParagraph(
		fmt.Sprintf("%s set up an account for you on %s.", inviter, product)))
	if note != "" {
		body.WriteString(`<p style="margin:0 0 16px;padding-left:12px;border-left:2px solid ` +
			mail.EmailColorBorder + `;font:italic 400 14px/1.6 ` + mail.EmailFont +
			`;color:` + mail.EmailColorMuted + `;">&ldquo;` + html.EscapeString(note) + `&rdquo;</p>`)
	}
	body.WriteString(mail.EmailButton("Set your password", claimURL))
	body.WriteString(mail.EmailFacts(
		mail.EmailFact{Label: "Sign in with", ValueHTML: html.EscapeString(email), Mono: true},
		mail.EmailFact{Label: "Link expires", ValueHTML: html.EscapeString(expiry)},
	))
	body.WriteString(mail.EmailLinkFallback(claimURL))

	return emailContent{
		Subject: fmt.Sprintf("%s invited you to %s", inviter, product),
		Text:    text.String(),
		HTML: mail.RenderLayout(mail.LayoutOptions{
			Preheader:  fmt.Sprintf("Choose a password and you're in — the link expires %s.", expiry),
			Title:      "You've been invited",
			BodyHTML:   body.String(),
			FooterHTML: html.EscapeString(fine),
		}),
	}
}
