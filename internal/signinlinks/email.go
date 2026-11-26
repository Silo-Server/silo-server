package signinlinks

import (
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/mail"
)

// emailContent is one rendered sign-in link email.
type emailContent struct {
	Subject string
	Text    string
	HTML    string
}

// composeEmail renders the sign-in link message. username is escaped
// anyway; linkURL is server-built.
func composeEmail(brand mail.Brand, username, serverName, linkURL string, expiresAt, now time.Time) emailContent {
	product := strings.TrimSpace(serverName)
	if product == "" {
		product = "Silo"
	}
	intro := fmt.Sprintf("An admin sent you a link to get back into your %s account.", product)
	how := "Open it on the device you want to use. It gives you a code for the Silo app, or lets you choose a password."
	expiry := mail.ExpiryPhrase(expiresAt, now)
	fine := fmt.Sprintf(
		"This link works once and expires %s. If you didn't ask an admin for it, ignore this email.", expiry)

	var text strings.Builder
	fmt.Fprintf(&text, "%s %s\n\n", intro, how)
	fmt.Fprintf(&text, "Open this link:\n\n  %s\n\n", linkURL)
	if username != "" {
		fmt.Fprintf(&text, "Account: %s\n\n", username)
	}
	fmt.Fprintf(&text, "%s\n", fine)

	var body strings.Builder
	body.WriteString(mail.EmailParagraph(intro + " " + how))
	body.WriteString(mail.EmailButton(brand, "Sign in", linkURL))
	facts := []mail.EmailFact{{Label: "Link expires", ValueHTML: html.EscapeString(expiry)}}
	if username != "" {
		facts = append([]mail.EmailFact{{Label: "Account", ValueHTML: html.EscapeString(username), Mono: true}}, facts...)
	}
	body.WriteString(mail.EmailFacts(facts...))
	body.WriteString(mail.EmailLinkFallback(linkURL))

	return emailContent{
		Subject: "Your sign-in link for " + product,
		Text:    text.String(),
		HTML: mail.RenderLayout(mail.LayoutOptions{
			Brand:      brand,
			Preheader:  fmt.Sprintf("Get back into your account — the link expires %s.", expiry),
			Title:      "Sign in again",
			BodyHTML:   body.String(),
			FooterHTML: html.EscapeString(fine),
		}),
	}
}
