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

`Message` carries recipients, subject, and text and/or HTML bodies (both set →
multipart/alternative). `Send` returns `mail.ErrNotConfigured` when email is
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

Admin UI: Admin Settings → Connections → Email, including a synchronous test
send (`POST /api/v1/admin/email/test`).

## Adding a consumer

Construct messages in the feature package and send through a `mail.Sender`
dependency. Do not read `email.*` settings from feature code, and check
`Enabled` (or branch on `ErrNotConfigured`) rather than treating a missing
SMTP configuration as an error.
