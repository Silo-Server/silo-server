package mail

import (
	"fmt"
	"html"
	"strings"
	"time"
)

// Shared visual tokens for Silo's branded emails, mirroring the web UI's
// default "Midnight Cinema" theme (web/src/app.css): a near-black canvas,
// monochrome type, and a white primary action. Feature packages compose body
// fragments with these tokens and wrap them with RenderLayout so every email
// the server sends looks like it came from the same product.
//
// Email-client constraints shape everything here: styles must be inline,
// layout must be tables, and colors must be explicit on every element (no
// inheritance through client-rewritten DOM). Web fonts don't load in most
// clients, so the stacks lead with the brand font and degrade to common
// system faces.
const (
	EmailFont     = "'Outfit','Avenir Next','Segoe UI',Helvetica,Arial,sans-serif"
	EmailFontMono = "'SF Mono',SFMono-Regular,Menlo,Consolas,'Liberation Mono',monospace"

	EmailColorCanvas = "#141417" // page background
	EmailColorCard   = "#1c1c20" // content card surface
	EmailColorBorder = "#2e2e35" // card outline
	EmailColorText   = "#e8e8ec" // primary text
	EmailColorMuted  = "#9696a0" // secondary text, badges, footer
	EmailColorRule   = "#26262c" // hairline row separators
	EmailColorAction = "#e8e8ec" // primary button background (white-on-dark)
	EmailColorOnAct  = "#141417" // primary button label
)

// LayoutOptions is the content RenderLayout places into the branded shell.
type LayoutOptions struct {
	// Preheader is the hidden inbox-preview snippet shown next to the subject
	// line. Plain text; optional.
	Preheader string
	// Title is the headline at the top of the card. Plain text; optional.
	Title string
	// BodyHTML is the card content below the title. Trusted HTML — callers
	// must escape any user-controlled values before building it.
	BodyHTML string
	// FooterHTML is the fine print under the card. Trusted HTML; optional.
	FooterHTML string
}

// RenderLayout wraps content in Silo's dark branded email shell: wordmark,
// content card, and footer. It adds no links of its own, so an email whose
// options carry no hrefs renders fully link-free (some features require
// that when no external URL is configured).
func RenderLayout(opts LayoutOptions) string {
	preheader := ""
	if opts.Preheader != "" {
		// The trailing zwnj/nbsp run pads the preview so clients don't pull
		// body markup into the snippet after the real preheader text.
		preheader = `<div style="display:none;max-height:0;overflow:hidden;mso-hide:all;">` +
			html.EscapeString(opts.Preheader) +
			strings.Repeat("&nbsp;&zwnj;", 40) + `</div>` + "\n"
	}
	title := ""
	if opts.Title != "" {
		title = `<h1 style="margin:0 0 16px;font:600 18px/1.4 ` + EmailFont +
			`;color:` + EmailColorText + `;">` + html.EscapeString(opts.Title) + `</h1>` + "\n"
	}
	footer := ""
	if opts.FooterHTML != "" {
		footer = `<tr><td style="padding:18px 6px 0;font:400 12px/1.7 ` + EmailFont +
			`;color:` + EmailColorMuted + `;">` + opts.FooterHTML + `</td></tr>` + "\n"
	}

	return strings.NewReplacer(
		"{{preheader}}", preheader,
		"{{title}}", title,
		"{{body}}", opts.BodyHTML,
		"{{footer}}", footer,
		"{{font}}", EmailFont,
		"{{canvas}}", EmailColorCanvas,
		"{{card}}", EmailColorCard,
		"{{border}}", EmailColorBorder,
		"{{text}}", EmailColorText,
	).Replace(emailShell)
}

// EmailButton renders the primary call-to-action: a white pill on the dark
// card, matching the web UI's primary action style. Both arguments are
// escaped here. The wrapping table keeps the button shape in Outlook, which
// ignores padding on anchors.
func EmailButton(label, href string) string {
	return `<table role="presentation" cellpadding="0" cellspacing="0" border="0"><tr>` +
		`<td bgcolor="` + EmailColorAction + `" style="background-color:` + EmailColorAction +
		`;border-radius:8px;mso-padding-alt:12px 24px;">` +
		`<a href="` + html.EscapeString(href) + `" style="display:inline-block;padding:12px 24px;` +
		`font:600 14px/1 ` + EmailFont + `;color:` + EmailColorOnAct +
		`;text-decoration:none;border-radius:8px;">` + html.EscapeString(label) + `</a>` +
		`</td></tr></table>`
}

// EmailParagraph renders one body paragraph in the standard text style,
// escaping the given plain text.
func EmailParagraph(text string) string {
	return `<p style="margin:0 0 16px;font:400 14px/1.6 ` + EmailFont +
		`;color:` + EmailColorText + `;">` + html.EscapeString(text) + `</p>`
}

// EmailFacts renders a bordered box of label/value rows, such as the address
// to sign in with and when a link expires. Each value is HTML the caller has
// already escaped; a mono value renders in the monospace face.
func EmailFacts(rows ...EmailFact) string {
	var b strings.Builder
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="margin:20px 0 0;border:1px solid ` +
		EmailColorBorder + `;border-radius:8px;">`)
	for _, row := range rows {
		valueFont := EmailFont
		if row.Mono {
			valueFont = EmailFontMono
		}
		b.WriteString(`<tr><td style="padding:10px 14px;font:400 13px/1.4 ` + EmailFont +
			`;color:` + EmailColorMuted + `;">` + row.Label + `</td>` +
			`<td align="right" style="padding:10px 14px;font:400 13px/1.4 ` + valueFont +
			`;color:` + EmailColorText + `;">` + row.ValueHTML + `</td></tr>`)
	}
	b.WriteString(`</table>`)
	return b.String()
}

// EmailFact is one row of EmailFacts.
type EmailFact struct {
	Label     string
	ValueHTML string
	Mono      bool
}

// EmailLinkFallback renders the "paste this link" line under a button for
// clients that block it.
func EmailLinkFallback(href string) string {
	return `<p style="margin:20px 0 0;font:400 12px/1.7 ` + EmailFont + `;color:` + EmailColorMuted +
		`;">Or paste this link into your browser:<br>` +
		`<span style="font:400 12px/1.7 ` + EmailFontMono + `;word-break:break-all;">` + html.EscapeString(href) + `</span></p>`
}

// ExpiryPhrase renders a link expiry as a human phrase ("in 7 days").
func ExpiryPhrase(expiresAt, now time.Time) string {
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

// emailShell is the document skeleton. The color-scheme meta plus explicit
// bgcolor attributes keep dark-mode-aware clients from inverting the design;
// the small stylesheet only tightens padding on narrow screens (supported by
// Gmail/Apple Mail, harmlessly ignored elsewhere).
const emailShell = `<!DOCTYPE html>
<html lang="en" style="color-scheme:dark;supported-color-schemes:dark;">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="dark">
<meta name="supported-color-schemes" content="dark">
<style>
  @media (max-width: 480px) {
    .silo-shell { padding: 24px 12px 36px !important; }
    .silo-card { padding: 24px 20px !important; }
  }
</style>
</head>
<body style="margin:0;padding:0;background-color:{{canvas}};" bgcolor="{{canvas}}">
{{preheader}}<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" bgcolor="{{canvas}}" style="background-color:{{canvas}};">
<tr><td align="center" class="silo-shell" style="padding:36px 16px 48px;">
<table role="presentation" width="560" cellpadding="0" cellspacing="0" border="0" style="width:100%;max-width:560px;">
<tr><td style="padding:0 6px 18px;font:600 12px/1 {{font}};color:{{text}};letter-spacing:7px;"><span style="color:#55555e;">&#9656;&#xFE0E;</span>&nbsp;&nbsp;SILO</td></tr>
<tr><td class="silo-card" bgcolor="{{card}}" style="background-color:{{card}};border:1px solid {{border}};border-radius:12px;padding:28px 32px;">
{{title}}{{body}}
</td></tr>
{{footer}}</table>
</td></tr>
</table>
</body>
</html>`
