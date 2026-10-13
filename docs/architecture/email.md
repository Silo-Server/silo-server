# Outbound Email (`internal/mail`)

**Status:** Implemented 2026-06-11

Silo's shared outbound email facility. It is deliberately feature-agnostic: any
feature that sends mail (notification emails, account flows, invites) composes
a `mail.Message` and hands it to the shared `mail.Sender`, so SMTP
configuration, security policy, and diagnostics live in exactly one place.

## Abstraction

```go
type Sender interface {
    Enabled(ctx context.Context) bool
    Send(ctx context.Context, msg Message) error
}
```

`Message` carries recipients, subject, text and/or HTML bodies (both set →
multipart/alternative), and optional inline images the HTML references as
`cid:<ContentID>` (multipart/related). `Send` returns `mail.ErrNotConfigured` when email is
disabled or incomplete, so features treat email as an optional transport and
degrade gracefully. The SMTP implementation (`mail.NewSMTPSender`) is backed by
`github.com/wneessen/go-mail`.

## Configuration

Live server settings (no restart required; read on every send — volume is
low):

| Key | Default | Notes |
|---|---|---|
| `email.enabled` | `false` | master switch |
| `email.smtp_host` | — | required |
| `email.smtp_port` | `587` | |
| `email.smtp_security` | `starttls` | `starttls` \| `tls` (implicit, port 465) \| `none` |
| `email.smtp_username` | — | empty = no auth |
| `email.smtp_password` | — | encrypted at rest (`SensitiveSettingKeys`) |
| `email.from_address` | — | required |
| `email.from_name` | `Silo` | |

Admin UI: Admin Settings → Notifications → Email, including a synchronous test
send (`POST /api/v1/admin/email/test`).

## Branded layout

HTML emails wrap their content with `mail.RenderLayout`, which draws the shared
dark shell. Its header follows the web sidebar: the server's uploaded wordmark,
else the Silo wordmark, with the server name as alt text. When
`branding.accent_color` is set, `mail.EmailButton` uses it for the primary
action and picks a dark or white label for contrast.

The logo is embedded in each message as an inline PNG, not linked. Remote images
are often blocked, the recipient may not reach the server, and uploaded logos
are stored as WebP, which Outlook does not render. `mail.BrandLoader` reads the
branding, converts the wordmark once per content ref, and falls back to the Silo
wordmark when the asset cannot be read, so branding never blocks a send. A
message rendered with a `mail.Brand` must carry `Brand.InlineImages()` as
`Message.Inline`. Retained messages, such as queued address verifications, keep
their logo so a retry sends exactly what was admitted.

## Adding a consumer

Construct messages in the feature package and send through a `mail.Sender`
dependency. Load the brand from the shared `mail.BrandLoader` and pass it to
the layout and `Message.Inline`. Do not read `email.*` settings from feature code, and check
`Enabled` (or branch on `ErrNotConfigured`) rather than treating a missing
SMTP configuration as an error.
