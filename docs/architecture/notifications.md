# Notifications

Silo's user-facing notification system treats media availability as durable,
server-side release events, fans them out to interested profiles as inbox
rows, and accelerates delivery through realtime websocket, web push, and
outbound webhooks. The durable `notification_deliveries` row is always the
source of truth; every other channel is best-effort on top of it. The code
lives in `internal/notifications` (distinct from the operational catalog/jobs
`Hub` in the same package).

## Release types

A release event records that an item became newly available in a library —
not that metadata says it aired, and not that the notifications feature first
saw it. Availability is a one-way fact: file churn (quality upgrades,
re-downloads) does not re-notify, and libraries emit no events until their
initial availability seeding completes.

Event kinds:

- `episode` — carries series/episode identity and fans out to interested
  profiles. An unset kind is normalized to `episode` (the column defaults to
  it; only in-memory events can be empty).
- Flat item kinds (`movie`, `audiobook`, `ebook`) — carry an item ID only and
  feed the server-channel broadcast feed; they do not fan out per profile.

Events can be suppressed instead of fanned out, with the reason recorded on
the row: `series_burst` when the per-series burst cap consumed the event
(bulk imports fan out only the highest few episode keys per series per
batch), or `stale` when the event aged past the fanout staleness horizon
(extended downtime, fanout disabled for a stretch) — delivering it long after
the fact would be noise.

The delivery `type` registry (`episode.available`, `webhook.auto_disabled`,
`request.*`, …) is extensible by construction; clients must render unknown
types with a generic fallback.

## Eligibility rules

Fanout evaluates each candidate `(profile, series)` interest row against the
release event's episode key:

- `favorite`, `watchlist`, and `continue_watching` notify on any newly
  available episode of the series.
- `next_up` notifies only when the episode is at or beyond the profile's
  `next_expected_episode_key`.
- Suppress when `last_notified_episode_key >= episode_key`.
- Profile-level notification preferences are a hard gate: a reason disabled
  in preferences can never match, so no delivery row is created and no
  channel — including webhooks — ever sees the event. Per-webhook reason
  filters can only narrow further, never re-enable.

Multiple matching reasons produce one delivery with merged reason flags.
Deliveries are deduplicated per `(profile, release event)` and, for
`episode.available`, per `(profile, episode)` across libraries, so
dual-quality library setups notify at most once. Reprocessing an event is
idempotent.

## Request notifications

Request lifecycle deliveries (`request.fulfilled`, `request.approved`,
`request.declined`) are operational notices posted directly to the requesting
profile — no interest index, no fanout. Their `reason_flags` carry request
identifiers (request ID, TMDB ID, media type; approved/declined also carry
the title since no catalog item exists yet) rather than the four reason
booleans. Partial unique indexes per `(profile_id, request_id, type)` make
the inserts idempotent, and the per-webhook `notify_requests` flag gates the
webhook channel for them.

## Outbound webhooks

Each profile can register up to a capped number of webhook destinations
(Discord or generic), with per-webhook reason filters. The profile chose the
URL, so notification content — titles, season/episode numbers — is included;
the guardrails below are the ones the profile cannot opt out of.

### Trust model and SSRF guard

Webhook deliveries are direct outbound HTTP from the user's own server, so
the destination URL is attacker-controllable input against that server's
network position:

- HTTPS only; TLS verification is not user-overridable.
- The destination host must never resolve to a private or special-purpose
  address. The deny set covers the IPv4 private/special ranges (loopback,
  RFC 1918, link-local, CGNAT, TEST-NETs, benchmarking, multicast, reserved)
  and the IPv6 equivalents (unspecified, loopback, ULA, link-local,
  documentation, NAT64). IPv4-mapped IPv6 addresses are unwrapped before
  checking so `::ffff:127.0.0.1` cannot bypass the IPv4 entries.
- The guard runs at registration *and* at connect time (the dialer
  re-validates the resolved address) to defeat DNS rebinding. Redirects are
  bounded and each hop is re-checked.
- An admin-only `notifications.webhooks.allow_private_destinations` setting
  exists for development environments.

Webhook URLs and generic signing secrets are encrypted at rest, never
returned by the API after creation (only the host is readable — a Discord
webhook URL is itself a bearer credential), and redacted in logs.

### Server URL leakage

Webhook payloads must not reveal the user's server origin. Discord fetches
embed thumbnail URLs, and the raw payload is visible to channel members, so
an absolute self-hosted artwork URL discloses the server's address to
Discord's infrastructure and to everyone in the channel. The rules:

- Embed builders only ever emit public provider origins (themoviedb.org,
  imdb.com, thetvdb.com and their image CDNs) by default. Presigned
  server-storage URLs appear only under the admin's explicit "server" poster
  mode opt-in (`System.discordPosterURL`).
- Builders never derive artwork URLs themselves; they render the poster URL
  the sender layer resolved under that policy.
- Generic payloads carry no server URL, no absolute artwork URLs, and no
  library name.

### Discord payload

Discord webhooks receive a native embed (no `content` line), with
`username: "Silo"` and a per-reason accent color (favorite, watchlist,
continue-watching, next-up precedence order). The builder enforces Discord's
embed limits — 256-char title, 4096-char description, 1024-char field
values, 2048-char footer, 6000 chars total — truncating description first
and never the title. Embeds cannot ping: Discord only parses mentions from
the top-level `content` field, which this payload never sets (the
`allowed_mentions` field is omitted rather than sent).

### Generic payload and HMAC signing

Generic webhooks receive canonical Silo JSON: event name, delivery and
webhook IDs, timestamp, `version`, `test` flag, profile ID, delivery type,
reason flags, and the series/episode (or request) content blocks.

Each request is signed with the webhook's per-destination 32-byte secret:

- Headers: `X-Silo-Event`, `X-Silo-Webhook-Id`, `X-Silo-Delivery-Id`,
  `X-Silo-Timestamp` (Unix epoch seconds), and
  `X-Silo-Signature: t=<epoch>,v1=<hex-hmac-sha256>` (Stripe's convention,
  so existing verification libraries work).
- The HMAC-SHA256 input is `{X-Silo-Timestamp}.{request body bytes}` — the
  literal bytes Silo sends. Receivers verify against the literal bytes they
  received with a constant-time compare and a replay window (~5 minutes); no
  JSON canonicalization is required on either side.
- The secret is returned once at create/rotate time and is never readable
  afterward. Rotation takes effect immediately with no dual-acceptance
  window; retried attempts re-sign with the current secret.

### Retry schedule and auto-disable

Delivery attempts are durable outbox rows committed in the fanout
transaction, so a crash between commit and dispatch delays a webhook instead
of dropping it (a recovery sweep claims stale pending rows). Each attempt has
a 10-second total timeout. Failures retry on an exponential schedule of
cumulative delays since the first attempt:

immediate, 30s, 2m, 10m, 30m, 2h, 6h, 12h, 18h, 24h — ten attempts total,
then the webhook is auto-disabled.

Non-retryable 4xx responses (everything except 408, 425, and 429) are
deterministic destination-side rejections: the attempt fails immediately
without walking the schedule. Auto-disable on that path requires three
*consecutive* deliveries to fail with a non-retryable 4xx, because
destination-side WAF/CDN blips intermittently 4xx valid webhooks. 429 honors
`Retry-After` when present. Auto-disable posts a `webhook.auto_disabled`
notice to the profile's inbox — a type that is itself excluded from webhook
dispatch so a broken webhook cannot loop.
