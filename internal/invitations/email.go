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
	expiry := expiryPhrase(expiresAt, now)

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
	body.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="margin:20px 0 0;border:1px solid ` +
		mail.EmailColorBorder + `;border-radius:8px;">` +
		factRow("Sign in with", html.EscapeString(email), true) +
		factRow("Link expires", html.EscapeString(expiry), false) +
		`</table>`)
	fmt.Fprintf(&body,
		`<p style="margin:20px 0 0;font:400 12px/1.7 %s;color:%s;">Or paste this link into your browser:<br>`+
			`<span style="font:400 12px/1.7 %s;word-break:break-all;">%s</span></p>`,
		mail.EmailFont, mail.EmailColorMuted, mail.EmailFontMono, html.EscapeString(claimURL))

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

// factRow renders one label/value line of the facts box.
func factRow(label, valueHTML string, mono bool) string {
	valueFont := mail.EmailFont
	if mono {
		valueFont = mail.EmailFontMono
	}
	return `<tr><td style="padding:10px 14px;font:400 13px/1.4 ` + mail.EmailFont +
		`;color:` + mail.EmailColorMuted + `;">` + label + `</td>` +
		`<td align="right" style="padding:10px 14px;font:400 13px/1.4 ` + valueFont +
		`;color:` + mail.EmailColorText + `;">` + valueHTML + `</td></tr>`
}

// expiryPhrase renders the expiry as a human phrase ("in 7 days").
func expiryPhrase(expiresAt, now time.Time) string {
	d := expiresAt.Sub(now)
	switch {
	case d <= 0:
		return "immediately"
	case d < 2*time.Hour:
		return "in 1 hour"
	case d < 48*time.Hour:
		return fmt.Sprintf("in %d hours", int(d.Round(time.Hour).Hours()))
	default:
		return fmt.Sprintf("in %d days", int(d.Round(24*time.Hour).Hours()/24))
	}
}
