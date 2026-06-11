# Notifications: Email Channel

**Date:** 2026-06-11
**Status:** Implemented (written post-implementation)
**Scope:** Item 3 of [`06-v1.5-roadmap.md`](./06-v1.5-roadmap.md) — the first real consumer of the shared SMTP core (`internal/mail`, `docs/architecture/email.md`).
**Depends On:** [`00-architecture-overview.md`](./00-architecture-overview.md), [`01-release-events-and-inbox.md`](./01-release-events-and-inbox.md)

## Decisions (resolving the roadmap's open questions)

- **Account-level, as planned.** Email addresses live on `users`; one mode
  covers every profile on the account and one email aggregates across them.
- **Per-episode AND digest, not digest-only.** The roadmap recommended
  digest-only; product direction chose to offer per-episode alerts too, gated
  by an admin allowance (`notifications.email.allow_per_episode`). When the
  admin disallows it, accounts set to per-episode are **coerced to the daily
  digest** rather than silenced.
- **Interest-scoped by construction.** Email consumes existing
  `notification_deliveries` rows, which the fanout only creates for profiles
  with series interest (favorites, watchlist, continue-watching, next-up) and
  for direct notices (`request.fulfilled`, `webhook.auto_disabled`). The
  channel adds no targeting of its own — it is never "all new content".
- **Opt-in, default off.** Users enable it per account in Settings →
  Notifications; enabling initializes the watermark to now so history never
  floods a fresh opt-in.

## Architecture: watermark sweep, not a third outbox

Webhooks and web push use per-target outbox attempt rows. Email deliberately
does not:

- Deliveries already carry `user_id`, and an account whose profiles follow the
  same series gets one row per profile — a per-row outbox would email the same
  episode several times. The sweep collapses them (dedupe by `episode_id`, by
  `request_id` for requests).
- A per-account watermark over `(created_at, id)` that advances **only after a
  successful SMTP send** gives durability for free: a crash or SMTP outage
  re-sends on the next pass instead of dropping.
- Both cadences are the same mechanism: per-episode sweeps every minute (and
  is nudged by the dispatcher seconds after fanout commits); the digest is the
  same sweep gated on "today's send hour passed and not yet stamped today".

State lives in `notification_email_prefs` (`migrations/sql/`,
`email_notification_channel`): mode, watermark, `last_digest_at`, and failure
backoff counters (`last_attempt_at`, `consecutive_failures`; 1m doubling,
capped at 6h). No FK to `users` per the notification-tables rule; deleted or
disabled accounts drop out of the recipient join. A supporting index
`notification_deliveries_user_created_idx (user_id, created_at, id)` serves
the sweep.

**Multi-node safety:** each account is processed inside one transaction that
claims the prefs row `FOR UPDATE SKIP LOCKED`, re-derives eligibility from the
locked row (mode flips and another node's digest stamp are both re-checked),
sends, then commits the watermark/stamp. Failed sends commit only the backoff
counters. `mail.ErrNotConfigured` aborts the whole pass; three consecutive
send failures end it early (SMTP trouble is global, not per-recipient).

**Flood bounds:** one email renders at most 30 lines (`…and N more in your
Silo inbox`), one pass fetches at most 200 rows per account, and upstream the
per-series burst cap already limits fanout volume. Digest emails include only
rows still unread at compose time; the watermark passes read rows silently.

## Files

| Piece | Location |
|---|---|
| Modes, prefs repo (`notification_email_prefs`) | `internal/notifications/email_prefs_repo.go` |
| Worker, dispatcher nudge, System service methods | `internal/notifications/email_digest.go` |
| Subject/text/HTML rendering | `internal/notifications/email_compose.go` |
| Account sweep query | `DeliveryRepository.ListForUserSince` (`internal/notifications/delivery_repo.go`) |
| Settings accessors | `internal/notifications/settings.go` |
| API handlers | `internal/api/handlers/notifications_email.go` (+ capability in `notifications.go`) |
| Web UI (user) | `EmailSection` in `web/src/pages/settings/NotificationsSettings.tsx` |
| Web UI (admin) | Email group in `web/src/pages/admin-settings/NotificationsAdminSettings.tsx` |
| Logic tests | `internal/notifications/email_logic_test.go` |

The worker is wired in `notifications.NewSystem` (new `mail.Sender` parameter,
passed from `cmd/silo/main.go`); its dispatcher joins the `MultiDispatcher`,
so operational deliveries (`request.fulfilled`, `webhook.auto_disabled`) nudge
it exactly like fanout rows do.

## Settings

| Key | Default | Meaning |
|---|---|---|
| `notifications.email_enabled` | `true` | Channel kill switch (availability still requires SMTP configured via `email.*`) |
| `notifications.email.allow_per_episode` | `true` | Admin allowance for the per-episode cadence |
| `notifications.email.digest_hour` | `8` | Hour (0–23, server-local) daily digests go out |
| `notifications.email.external_url` | empty | Public base URL for deep links in emails; empty sends link-free emails (the server origin is never leaked implicitly) |

## API

- `GET /api/v1/notifications/email-preferences` → `{"mode": "off" | "per_episode" | "daily_digest"}`
- `PUT /api/v1/notifications/email-preferences` `{"mode": ...}` — 400 codes:
  `bad_request` (unknown mode), `not_allowed` (per-episode disallowed),
  `no_email` (account has no address). Any profile on the account may set it.
- `GET /api/v1/notifications/capability` gained
  `"email": {"available", "modes", "digest_hour"}`; clients gate setup UI on
  it as usual. `available` requires the kill switch on **and**
  `mail.Sender.Enabled()` — never read `email.*` settings directly.

## Deliberately not in v1

- Posters/images in emails (would require externally reachable presigned URLs).
- `List-Unsubscribe` headers / tokenized unsubscribe endpoint (self-hosted,
  opt-in; revisit if servers grow beyond household scale).
- Per-user digest hour (admin-global for now).
- DB-backed integration tests for the sweep (same Postgres-harness gap as the
  rest of `01`'s verification backlog; pure logic is covered by
  `email_logic_test.go`).
