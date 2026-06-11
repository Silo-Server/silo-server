# Outbound Webhooks Spec

**Date:** 2026-04-28
**Status:** Draft
**Scope:** Profile-scoped outbound webhook destinations for Silo notifications. Native Discord embed type and generic JSON+HMAC type.
**Depends On:**
- [`00-architecture-overview.md`](./00-architecture-overview.md)
- [`01-release-events-and-inbox.md`](./01-release-events-and-inbox.md)

## Summary

Silo should let each profile send their notifications to user-chosen webhook destinations: a Discord channel, a Slack incoming webhook, a personal automation service, or any HTTP endpoint that accepts JSON. Webhooks are configured per profile, support per-webhook reason filters, and include full notification content by default — the profile chose the destination, so trust is implicit.

Two webhook types ship in v1:

- **Discord** — native Discord embed payload. The user pastes a Discord channel webhook URL; Silo POSTs Discord-formatted embeds.
- **Generic** — canonical Silo JSON, HMAC-SHA256 signed via `X-Silo-Signature`. Suitable for Slack-incoming-webhook-style targets, custom user automations, or any HTTP endpoint.

## Why this is different from APNs / FCM

The privacy model inverts. APNs and FCM relay paths route through infrastructure operators (Apple, Google, Silo's relay) that the user did not directly choose. Therefore those paths carry no notification content — only opaque IDs.

Outbound webhooks go to a destination the **profile explicitly configured**. By choosing the URL, the profile is consenting to send notification content to that endpoint. There is no privacy benefit to gating content out of webhooks the profile set up themselves; doing so would just make the feature useless ("New episode! [check Silo]" is no better than what's already in the app).

The trust model is therefore:

- Profile chose the URL → notification content is included by default.
- Discord embeds get series title, episode title, season/episode numbers, and poster.
- Generic webhooks get the same data in canonical JSON, plus an HMAC signature derived from a per-webhook signing secret.
- Silo still enforces privacy guardrails the profile cannot opt out of: HTTPS only, no localhost / RFC1918 destinations by default, redacted logs, retry/disable on persistent failure.

## Decision

Add per-profile webhook destinations, configurable through the profile's notification preferences UI:

- Profile creates a webhook by pasting a URL and optionally a name and reason filters.
- Webhook type is detected from the URL or selected explicitly: `discord` (matches Discord webhook URL pattern) or `generic`.
- Silo POSTs a typed payload to the URL whenever a `notification_deliveries` row commits for that profile and the webhook's reason filters allow it.
- Failed deliveries are retried with exponential backoff and disabled after a configurable consecutive-failure threshold.
- Each webhook has a profile-visible status: enabled, last success, last failure code, consecutive failure count.

## Goals

- Let a profile send Silo notifications to Discord, Slack, or any user-chosen URL.
- Render natively in Discord without the user copy-pasting JSON templates.
- Provide a stable, signed, well-documented generic format for everything else.
- Keep the feature profile-scoped: a profile only sees and controls their own webhooks.
- Add no new infrastructure dependencies (no relay needed; webhooks are direct outbound HTTP from the user's Silo server).
- Make failure modes legible to the profile so they can fix a broken webhook themselves.

## Non-Goals

- Server-wide / admin-level webhooks for system events (scan complete, library health, etc.). Out of scope v1; Silo's existing realtime hub already handles operational events.
- Webhook destinations that require OAuth (Slack apps, Discord bots, Microsoft Teams Adaptive Cards). Webhook-style endpoints with an unauth'd POST URL are sufficient for v1.
- Per-event-type custom payload templates. The Discord embed shape and the generic JSON shape are fixed in v1; richer customization is v2.
- Webhook retry windows beyond 24 hours. After ~24h of consecutive failures, the webhook is auto-disabled and the profile is notified in-app.
- Multiple webhook destinations per webhook row (e.g., one row firing to two URLs). One URL per row.
- Two-way webhooks. Silo only POSTs out; it does not consume webhook responses for state.

## Trust Model

The profile chose the destination URL. By creating a webhook, the profile consents to send full notification content (titles, posters, episode metadata) to that URL.

What we still don't trust the profile about:

- **HTTPS:** Required. `http://` URLs are rejected.
- **Local / private destinations:** Rejected by default. Specifically, the URL host must resolve to an address outside all of the following ranges:
  - **IPv4 private/special:** `0.0.0.0/8`, `10.0.0.0/8`, `100.64.0.0/10` (CGNAT / RFC6598), `127.0.0.0/8`, `169.254.0.0/16`, `172.16.0.0/12`, `192.0.0.0/24` (IETF protocol assignments), `192.0.2.0/24` / `198.51.100.0/24` / `203.0.113.0/24` (TEST-NET-1/2/3), `192.88.99.0/24` (deprecated 6to4 anycast), `192.168.0.0/16`, `198.18.0.0/15` (benchmarking, RFC 2544), `224.0.0.0/4` (multicast), `240.0.0.0/4` (reserved future use)
  - **IPv6 private/special:** `::/128` (unspecified), `::1/128` (loopback), `fc00::/7` (ULA), `fe80::/10` (link-local), `2001:db8::/32` (documentation), `64:ff9b::/96` (NAT64)
  - **IPv4-mapped IPv6:** `::ffff:0:0/96` — the most likely real-world bypass. A literal `::ffff:127.0.0.1` resolves to loopback when the connection is established but bypasses naive IPv4-only checks. The validator must unwrap v4-mapped addresses and re-check against the IPv4 deny set.
  - **DNS names that resolve to any of the above** at registration time *and* at delivery time (DNS rebinding mitigation; see below).

  An admin-only setting `notifications.webhooks.allow_private_destinations = true` may be set for dev environments. It applies globally to the server and is intended only for development.
- **Server URL leakage:** Webhook payloads must **not** include the user's Silo server hostname, base URL, or absolute artwork URLs that include the server origin. Posters in Discord embeds use a profile-token-signed proxy URL (or, for v1, the embed omits images if the server URL would leak). Generic webhooks include only relative paths and let the receiver fetch from the server using their own knowledge of the server URL.
- **Relay leakage:** The webhook delivery path runs entirely on the user's own Silo server; no Silo-operated infrastructure is involved.

## User Flow

```
1. Profile opens Settings -> Notifications -> Webhooks.
2. Profile clicks "Add webhook".
3. Pastes a URL like https://discord.com/api/webhooks/123/abc.
4. Silo auto-detects Discord type from URL pattern.
5. Profile names the webhook ("Family Discord") and toggles
   reason filters: favorites on, watchlist on, continue_watching off, next_up off.
6. Profile clicks "Test", Silo POSTs a sample notification.
7. Discord channel shows the test embed; profile clicks "Save".
8. From now on, matching new-episode notifications post to Discord.
9. If the webhook URL becomes invalid (Discord deletes it), Silo:
    a. Marks each 404'd delivery failed immediately (non-retryable 4xx);
       transient network failures instead retry with backoff for ~24h.
    b. After 3 consecutive non-retryable-4xx deliveries (or the retry
       threshold for persistent network failures), auto-disables the webhook.
    c. Posts an in-app notification to the profile: "Your 'Family Discord'
       webhook stopped working. Last error: 404. Edit settings to fix."
```

## Data Model

### `notification_webhooks`

Purpose: profile-scoped webhook destination.

Columns:

- `id text primary key`
- `user_id integer not null` (matches `users.id integer`)
- `profile_id text not null`
- `name varchar(64) not null` — user-friendly label, capped to bound row size
- `type text not null` — `'discord'` or `'generic'`
- `url_ciphertext bytea not null` — destination URL, encrypted at rest
- `url_host varchar(253) not null` — denormalized host (no path) for diagnostics and validation logs (253 = max DNS host length)
- `signing_secret_ciphertext bytea` — null for `discord` (Discord webhooks don't sign); required for `generic`
- `enabled boolean not null default true`
- `notify_favorites boolean not null default true`
- `notify_watchlist boolean not null default true`
- `notify_continue_watching boolean not null default true`
- `notify_next_up boolean not null default true`
- `consecutive_failures integer not null default 0`
- `disabled_reason varchar(256)` — populated when auto-disabled
- `last_success_at timestamptz`
- `last_failure_at timestamptz`
- `last_failure_status integer`
- `last_failure_message varchar(256)` — short, non-sensitive diagnostic, e.g., "404 Not Found"
- `created_at timestamptz not null default now()`
- `updated_at timestamptz not null default now()`

Constraints:

- `(profile_id, name)` is unique per profile to prevent duplicate-name confusion.
- `CHECK (type IN ('discord', 'generic'))`.
- `CHECK (type = 'discord' OR signing_secret_ciphertext IS NOT NULL)` — generic webhooks must have a signing secret.

Indexes:

- `(profile_id)` for the listing endpoint.
- `(profile_id, enabled)` partial index where `enabled = true` for the dispatcher hot path.

Notes:

- URLs and signing secrets are encrypted using the same local secret-storage facility as other sensitive columns (e.g., APNs tokens). If that facility doesn't exist yet, this spec lands the columns as plain `bytea` containing UTF-8 bytes for v1; an explicit follow-up adds the encryption layer.
- `disabled_reason` is set when the system auto-disables; cleared when the profile re-enables.

### `webhook_delivery_attempts`

Purpose: operational record of webhook attempts.

Columns:

- `id text primary key`
- `notification_delivery_id text not null references notification_deliveries(id) on delete cascade`
- `webhook_id text not null references notification_webhooks(id) on delete cascade`
- `attempt_number integer not null`
- `attempted_at timestamptz not null default now()`
- `next_retry_at timestamptz`
- `http_status integer`
- `outcome text not null` — `'pending'`, `'delivered'`, `'retrying'`, `'failed'`, `'auto_disabled'`
- `failure_message varchar(256)` — short diagnostic (e.g., HTTP status text, DNS error class). Must not include payload contents.

Constraints:

- `(webhook_id, notification_delivery_id, attempt_number)` is unique to prevent double-claims under retry concurrency.
- `CHECK (outcome IN ('pending', 'delivered', 'retrying', 'failed', 'auto_disabled'))`.
- index on `(webhook_id, attempted_at desc)` for per-webhook history listing.
- index on `(outcome, next_retry_at)` for the retry worker.

Retention:

- keep `delivered` rows for ~7 days
- keep `failed`/`auto_disabled` rows for ~30 days for profile-visible debugging
- keep `pending`/`retrying` rows until they resolve

## Server API Surface

All endpoints are profile-scoped and require `X-Profile-Id` middleware.

### List Webhooks

```http
GET /api/v1/notifications/webhooks
```

Response:

```json
{
  "webhooks": [
    {
      "id": "01J...",
      "name": "Family Discord",
      "type": "discord",
      "url_host": "discord.com",
      "enabled": true,
      "notify_favorites": true,
      "notify_watchlist": true,
      "notify_continue_watching": false,
      "notify_next_up": false,
      "last_success_at": "2026-04-27T18:32:11Z",
      "last_failure_at": null,
      "last_failure_status": null,
      "last_failure_message": null,
      "consecutive_failures": 0,
      "disabled_reason": null
    }
  ]
}
```

Note the response **never includes the full URL or signing secret**. It returns only `url_host` so the profile can identify which Discord/Slack/etc. the webhook points at without leaking the secret URL token (which functions as the auth credential for Discord webhooks).

### Create Webhook

```http
POST /api/v1/notifications/webhooks
```

Request:

```json
{
  "name": "Family Discord",
  "url": "https://discord.com/api/webhooks/123456/abcdef-token",
  "type": "discord",
  "notify_favorites": true,
  "notify_watchlist": true,
  "notify_continue_watching": false,
  "notify_next_up": false
}
```

Rules:

- `type` is optional; if omitted, the server detects Discord URLs via pattern (`https://discord.com/api/webhooks/{id}/{token}` or `https://discordapp.com/...`). All other URLs default to `generic`.
- For `generic`, the server generates a random signing secret (32 bytes, base64) and returns it **once** in the response. The profile is responsible for storing it on the receiving service.
- URL must pass private-destination guards.
- URL must use `https`.
- `name` length capped at 64 chars; reject empty or whitespace-only.
- Per-profile cap: max 10 webhooks (configurable via admin setting; documented as `notifications.webhooks.max_per_profile`).

Response:

```json
{
  "id": "01J...",
  "name": "Family Discord",
  "type": "discord",
  "url_host": "discord.com",
  "enabled": true,
  "notify_favorites": true,
  "notify_watchlist": true,
  "notify_continue_watching": false,
  "notify_next_up": false,
  "signing_secret": null
}
```

For `generic`:

```json
{
  "id": "01J...",
  "name": "My Slack Webhook",
  "type": "generic",
  "url_host": "hooks.slack.com",
  "enabled": true,
  "notify_favorites": true,
  "notify_watchlist": true,
  "notify_continue_watching": true,
  "notify_next_up": true,
  "signing_secret": "base64-encoded-secret-shown-once"
}
```

`signing_secret` is **only** returned at create time and is never re-fetchable. Rotation requires a separate endpoint.

### Update Webhook

```http
PUT /api/v1/notifications/webhooks/{id}
```

Request fields are all optional; included fields are updated:

```json
{
  "name": "Renamed",
  "enabled": true,
  "notify_favorites": false,
  "url": "https://discord.com/api/webhooks/.../new-token"
}
```

Updating the URL re-validates against the private-destination guard and resets `consecutive_failures` to 0.

### Delete Webhook

```http
DELETE /api/v1/notifications/webhooks/{id}
```

Idempotent. Cascades to `webhook_delivery_attempts`.

### Rotate Signing Secret (generic only)

```http
POST /api/v1/notifications/webhooks/{id}/rotate-secret
```

Generates and returns a new signing secret. The profile must update the receiving service to use the new secret.

### Test Webhook

```http
POST /api/v1/notifications/webhooks/{id}/test
```

Synchronously POSTs a sample payload to the destination and returns the result:

```json
{
  "ok": true,
  "http_status": 204,
  "duration_ms": 187
}
```

or:

```json
{
  "ok": false,
  "http_status": 404,
  "duration_ms": 234,
  "message": "404 Not Found"
}
```

The test payload is identical in shape to a real notification but is clearly marked (e.g., embed footer text "Silo test notification" for Discord; `"test": true` field in generic). Test sends do not consume the retry/auto-disable counters.

## Payload Formats

### Discord

The Discord webhook API documents the `POST /api/webhooks/{id}/{token}` endpoint, which accepts a JSON body with optional `content` (plain text) and `embeds` (rich cards). Silo sends embeds only; no `content` field, so the message renders cleanly without a leading text line.

**v1 payload (text-only, no images):**

```json
{
  "embeds": [
    {
      "title": "Severance — S2 E1: Hello, Ms. Cobel",
      "description": "New episode available on Silo",
      "color": 5814783,
      "footer": {
        "text": "Silo • Severance"
      },
      "timestamp": "2026-04-28T12:34:56Z",
      "fields": [
        { "name": "Reason", "value": "Favorited & Continue Watching", "inline": true },
        { "name": "Season", "value": "2", "inline": true },
        { "name": "Episode", "value": "1", "inline": true }
      ]
    }
  ],
  "username": "Silo"
}
```

**v1.5 payload (with image proxy, additive):**

In v1.5, the builder adds `image` (poster) and `avatar_url` (Silo mark) fields, both pointing at `media.discord-cdn-proxy.silo.app` URLs. **In v1, these fields MUST be omitted entirely** — the builder must not fall back to absolute URLs that include the user's server origin. The privacy contract is broken if Discord sees the user's server URL in any embed field.

Notes on the Discord payload:

- **v1 builder rule:** the `image` field, the embed's `url` field, and the top-level `avatar_url` field must be omitted. Discord renders the embed without an image; the channel webhook's configured avatar is used. There is no fallback to the user's server URL.
- **v1.5 builder rule:** `image.url` and `avatar_url` use a CDN proxy hosted at `media.discord-cdn-proxy.silo.app`. The proxy is a small Silo-operated service that takes a short-lived signed URL token (issued by the user's server when constructing the webhook payload) and server-side streams the corresponding asset from the user's server. The leak vector being mitigated is *Discord's own infrastructure* fetching and caching the embed image — Discord's CDN (`media.discordapp.net`) fetches once from whatever origin the embed names; end-users only ever see the cached `media.discordapp.net` URL. The proxy ensures the origin Discord fetches from is the proxy, not the user's server.
- **Discord embed limits** (apply to both v1 and v1.5; payload builder must enforce):
  - 6,000 char total across `title` + `description` + `field.name` + `field.value` + `footer.text` per embed
  - `title`: 256 chars
  - `description`: 4,096 chars
  - `fields`: 25 max; `field.name` 256 chars, `field.value` 1,024 chars
  - `footer.text`: 2,048 chars
  - 10 embeds per message (Silo sends one)
  - **Truncation policy:** prefer to truncate `description` first (with ellipsis), then field values. Never truncate `title`. If even truncated content exceeds the 6,000-char total, drop fields right-to-left until under cap.
- `color` is a Discord embed accent color encoded as decimal RGB. Silo picks per-reason: favorite=5814783 (Silo brand purple), watchlist=3066993 (green), continue_watching=15844367 (yellow), next_up=15158332 (red).
- `username` overrides the Discord webhook's default name to "Silo".
- The webhook URL itself is the auth — Discord's webhook tokens are bearer credentials in the URL path. This is why we never return the URL on read.

The Discord embed structure follows Discord's webhook API. See [Discord embed object docs](https://discord.com/developers/docs/resources/channel#embed-object) for the full schema.

### Generic

JSON body, signed via HMAC-SHA256 over the raw bytes Silo sends. Receivers verify against the literal bytes they received — no canonicalization required on either side.

Headers:

- `Content-Type: application/json`
- `User-Agent: Silo-Webhook/1.0`
- `X-Silo-Event: notification.created`
- `X-Silo-Webhook-Id: 01J...` — the webhook row ID
- `X-Silo-Delivery-Id: 01J...` — the underlying `notification_deliveries.id`
- `X-Silo-Timestamp: 1714299296` — Unix epoch seconds, integer
- `X-Silo-Signature: t=1714299296,v1=<hex-hmac-sha256>`
  - HMAC-SHA256 of the byte string `{X-Silo-Timestamp}.{request body bytes}` using the per-webhook signing secret. The result is hex-encoded.
  - Format follows Stripe's signing convention so receivers can use existing libraries (Stripe Go SDK, `stripe-signature` ports, etc.).
  - **The timestamp value in the body's `timestamp` field is informational and may be RFC3339 for human readability; only the `X-Silo-Timestamp` header value (Unix epoch integer) participates in the HMAC computation.**

Body:

```json
{
  "event": "notification.created",
  "delivery_id": "01J...",
  "webhook_id": "01J...",
  "timestamp": "2026-04-28T12:34:56Z",
  "version": 1,
  "test": false,
  "profile_id": "profile-1",
  "library_id": 7,
  "type": "episode.available",
  "reason_flags": {
    "favorite": true,
    "watchlist": false,
    "continue_watching": true,
    "next_up": true
  },
  "series": {
    "id": "series-123",
    "title": "Severance"
  },
  "episode": {
    "id": "episode-456",
    "title": "Hello, Ms. Cobel",
    "season_number": 2,
    "episode_number": 1
  }
}
```

Notes on the generic payload:

- The body is canonicalized (sorted keys, UTF-8 encoded) before HMAC computation so the receiver can verify deterministically.
- `profile_id` is included so the receiver can route by profile (one Slack channel per profile, etc.). It is the profile's own ID and is acceptable to share with the destination the profile chose.
- No server URL, no absolute poster URLs, no library name. The receiver cannot link this back to the user's server hostname unless the profile has separately told them.
- `version: 1` allows future schema changes without breaking existing receivers.
- `test: true` distinguishes test sends from real sends.

### Signature Verification (generic)

Receivers verify by:

```
parts = X-Silo-Signature.split(",")
ts = parts.find("t=").value           // Unix epoch integer (string form)
v1 = parts.find("v1=").value          // hex string

raw_body = literal request body bytes (do NOT re-parse and re-serialize JSON)
expected = hex(hmac_sha256(secret, ts + "." + raw_body))

if constant_time_compare(v1, expected) and abs(now_epoch - parse_int(ts)) < 300:
    accept
else:
    reject
```

Key rules:

- Sign and verify the **literal request body bytes**, not a re-canonicalized form. This eliminates JSON-canonicalization ambiguity (number formatting, escape rules, key ordering) and matches Stripe's pattern.
- `ts` and `now_epoch` are both Unix epoch seconds (integer). The timestamp window of 300 seconds (5 minutes) prevents replay; receivers may tighten if they wish.
- Use a constant-time comparator (`hmac.equal`, `crypto/subtle.ConstantTimeCompare`, etc.) to avoid timing attacks.

### Signing Secret Rotation

`POST /api/v1/notifications/webhooks/{id}/rotate-secret` generates a new signing secret and returns it once. After rotation:

- The new secret takes effect immediately for all subsequent deliveries.
- **Pending and retrying delivery attempts re-sign with the current secret on each retry**, not the secret that was active when the attempt was first enqueued. This means receivers who have updated to the new secret will accept the retried delivery; receivers still on the old secret will reject it. The expected operational pattern is: profile rotates the secret and updates their receiver atomically (or near-atomically); brief retry-window mismatches are acceptable.
- Silo does **not** keep the old secret around after rotation. There is no dual-acceptance window.

## Delivery Semantics

### Send path

1. The fanout transaction in [`01-release-events-and-inbox.md`](./01-release-events-and-inbox.md) commits the `notification_deliveries` row **and**, in the same transaction, one `webhook_delivery_attempts` row with `outcome = 'pending'` per enabled, reason-matching webhook (the dispatch outbox). Two filters apply at enqueue time:
   - **Type deny list.** `webhook.auto_disabled` deliveries never enqueue webhook attempts — a webhook auto-disable notice must never be re-dispatched as a webhook, otherwise a broken webhook would generate an auto-disable notification that fires another webhook attempt that fails again, looping. Other notification types may join this list as they're added.
   - **Per-webhook reason filter** (`notify_favorites`, etc.). No matching reason, no attempt row.
2. `WebhookDispatcher.Dispatch(delivery)` runs post-commit and claims the delivery's `pending` attempt rows (`FOR UPDATE SKIP LOCKED`).
3. For each claimed attempt:
   - Construct the payload (Discord embed or generic JSON).
   - POST the payload with a 10-second total timeout.
   - On success (HTTP 2xx): mark attempt `delivered`, update webhook `last_success_at`, reset `consecutive_failures = 0`.
   - On failure: mark attempt `retrying` with `next_retry_at`, increment `consecutive_failures` on the webhook.
4. The retry worker reads `webhook_delivery_attempts WHERE (outcome = 'retrying' AND next_retry_at <= now()) OR (outcome = 'pending' AND attempted_at <= now() - interval '60 seconds')` — the second arm is outbox recovery for attempts whose post-commit dispatch never ran (process crash).
5. When `consecutive_failures` exceeds the threshold (default 10) over a span > 24h, the webhook is auto-disabled (`enabled = false`, `disabled_reason = '...'`), and an in-app notification is posted to the profile via the in-app inbox.

### Preference precedence (relationship to `notification_preferences`)

Profile-level `notification_preferences.notify_*` flags are a **hard gate** applied during fanout (see [`01-release-events-and-inbox.md`](./01-release-events-and-inbox.md)). A delivery row is never created if all matching reasons are disabled at the profile level — so the dispatcher never sees those events.

Per-webhook `notify_*` flags are an **additional filter** applied during dispatch. They can narrow what fires for a specific webhook, but they cannot re-enable a reason the profile has globally disabled. The frontend must reflect this: if a profile turns off `notify_continue_watching` globally, the per-webhook continue-watching checkbox should appear disabled with explanatory text rather than implying the user can re-enable it for one destination.

### Retry schedule

Exponential backoff, capped at 24h total:

| Attempt | Delay since first attempt |
|---|---|
| 1 | 0 (immediate) |
| 2 | 30s |
| 3 | 2m |
| 4 | 10m |
| 5 | 30m |
| 6 | 2h |
| 7 | 6h |
| 8 | 12h |
| 9 | 18h |
| 10 | 24h, then auto-disable |

Each attempt has a 10s total timeout.

**4xx skip-retry:** 4xx HTTP responses (except `408 Request Timeout`, `425 Too Early`, `429 Too Many Requests`) are deterministic destination-side rejections — retrying *this delivery* won't help, so the attempt is marked `failed` immediately without walking the 10-attempt schedule. Auto-disable, however, requires **3 consecutive deliveries** to fail with a non-retryable 4xx (tracked via `consecutive_failures` plus the failure-status class) before the webhook is disabled. A single 4xx is not proof the webhook is dead: destination-side WAF/CDN blips (Discord behind Cloudflare) intermittently return 403/400 for valid webhooks, and instant disable on one such blip would force pointless manual re-enables. Three consecutive deterministic rejections is strong evidence the URL is actually gone. The profile is notified in-app per "Auto-Disable Notification" below.

`429 Too Many Requests` honors the `Retry-After` header if present (overriding the schedule above for that attempt).

### Concurrency

The dispatcher fans webhook deliveries in parallel within a small bounded pool (e.g., 16 workers per server process). One slow webhook destination cannot block other deliveries.

### Test delivery isolation

`POST /webhooks/{id}/test` sends synchronously, does not write to `webhook_delivery_attempts`, does not affect `consecutive_failures`, and returns the HTTP result inline.

## Privacy Requirements

- Webhook URLs are encrypted at rest where the local encryption facility supports it.
- Webhook URLs are never returned in API responses (only `url_host`).
- Generic signing secrets are returned only at create / rotate time; never readable after.
- Logs redact full URLs (host only) and full signing secrets.
- Logs do not persist webhook payload bodies. Failure logs may include HTTP status text.
- Webhook payloads must not include the Silo server URL or absolute artwork URLs that include the server origin.
- An admin must not be able to read another profile's webhook URLs or signing secrets.

## Security Requirements

- HTTPS-only destinations.
- Private-destination guard on URL submission and on each delivery (re-resolve to catch DNS rebinding to private IPs).
- HMAC-SHA256 with per-webhook 32-byte signing secrets for `generic`.
- Signing secret returned only at create / rotate; rotation immediately invalidates the previous secret.
- Per-profile cap on webhook count (default 10).
- Per-profile rate limit on webhook delivery (e.g., 60 deliveries / 60 seconds / profile across all webhooks). Notifications that exceed the limit are still durably stored in the inbox; webhooks just don't fire for them. Logged so admins can spot runaway scenarios.
- TLS verification is enforced and not user-overridable.
- HTTP redirects are followed (max 3 hops) but the final URL must still pass the private-destination guard at each hop.

## DNS Rebinding Mitigation

A naive implementation that resolves the URL once at registration time is vulnerable to DNS rebinding: the host could resolve to a public IP at registration and a private IP at delivery time. Mitigation:

- At delivery time, resolve the host yourself, validate the resolved IPs against the private deny set, then connect using the validated IP (or a controlled `net.Dialer` that re-validates the address it's about to connect to).
- Do not let the standard library transparently re-resolve on each request.
- The `internal/notifications/webhook_http.go` HTTP client should use a custom `Dialer.Control` callback that inspects the resolved address and refuses connections to private ranges.

## Settings And UX

Profile-level settings:

```text
Webhooks

Add webhook
  Send Silo notifications to a webhook URL.
  Discord URLs render as native Discord embeds.
  Other URLs receive signed JSON.

[Family Discord]                              ✓ enabled
  discord.com
  ✓ Favorites  ✓ Watchlist  ☐ Continue watching  ☐ Next up
  Last success: 2 hours ago
  [ Test ]  [ Edit ]  [ Delete ]

[Slack: #media]                               ✓ enabled
  hooks.slack.com
  ✓ all reasons
  Last failure: 3 minutes ago — 404 Not Found
  ⚠ This webhook is failing. Check the destination URL.
  [ Test ]  [ Edit ]  [ Delete ]

+ Add webhook
```

Admin-level settings (server-wide guards, default values):

- `notifications.webhooks.max_per_profile` (default 10)
- `notifications.webhooks.allow_private_destinations` (default false; for dev environments)
- `notifications.webhooks.deliveries_per_minute_per_profile` (default 60)

## Implementation Plan

### Task 1: Schema

Files:

- new migration under `migrations/` adding `notification_webhooks` and `webhook_delivery_attempts` tables.

### Task 2: Webhook Repository And Service

Files:

- `internal/notifications/webhook_repo.go`
- `internal/notifications/webhook_service.go` (CRUD, validation, signing-secret handling)

Includes:

- URL validation (https, private-destination guard)
- Discord URL detection
- signing secret generation
- per-profile count enforcement

### Task 3: HTTP Client With Address Guard

Files:

- `internal/notifications/webhook_http.go`

Custom HTTP client with:

- 10-second timeout
- TLS verification non-overridable
- `Dialer.Control` callback that re-validates the resolved IP at connect time
- bounded redirect handling
- structured error reporting (DNS error class, TCP, TLS, HTTP status) for `failure_message`

### Task 4: Payload Builders

Files:

- `internal/notifications/webhook_payload_discord.go`
- `internal/notifications/webhook_payload_generic.go`

Each pure function `func Build(delivery NotificationDelivery, hook Webhook) ([]byte, error)`. No side effects, easy to unit test.

### Task 5: Dispatcher Integration

Files:

- `internal/notifications/webhook_dispatcher.go`

Implements the channel `Dispatcher` interface from `00-architecture-overview.md`. Loads enabled webhooks for the delivery's profile, applies reason filters, posts payloads, records attempts.

### Task 6: Retry Worker

Files:

- `internal/notifications/webhook_retry_worker.go`

Polls `webhook_delivery_attempts WHERE outcome = 'retrying' AND next_retry_at <= now()` with `FOR UPDATE SKIP LOCKED`. Re-dispatches and updates state. Handles auto-disable.

### Task 7: API Handlers And Routes

Files:

- `internal/api/handlers/notifications_webhooks.go`
- `internal/api/router.go`

Routes:

- `GET /api/v1/notifications/webhooks`
- `POST /api/v1/notifications/webhooks`
- `PUT /api/v1/notifications/webhooks/{id}`
- `DELETE /api/v1/notifications/webhooks/{id}`
- `POST /api/v1/notifications/webhooks/{id}/test`
- `POST /api/v1/notifications/webhooks/{id}/rotate-secret`

All require `RequireProfile` middleware.

### Task 8: Frontend

Files:

- `web/src/pages/settings/NotificationWebhooksSettings.tsx`
- `web/src/hooks/queries/notification-webhooks.ts`
- form components, status pills, test-result display

Includes:

- "Show signing secret once" UX with explicit "I've saved it" confirmation.
- Show secret-rotation flow.
- Last-failure status with admin-level message ("404 Not Found"), not stack traces.
- Test button.
- Per-reason filter checkboxes.

### Task 9: Auto-Disable Notification

When auto-disable fires, create a `notification_deliveries` row with `type = "webhook.auto_disabled"`, including the webhook name and last failure code. The profile sees this in their inbox so a broken webhook doesn't fail silently.

### Task 10: Optional CDN Proxy For Discord Images

Files:

- separate repo (e.g., `silo-discord-cdn-proxy`)
- `internal/notifications/webhook_image_signer.go` for issuing short-lived signed URL tokens

Out of scope for v1 unless Discord image rendering is wanted in the first release. v1 ships embeds without images; v1.5 adds the proxy and image URLs.

## Validation Plan

- Unit tests for URL validators (HTTPS, private-IP guard, DNS resolution).
- Unit tests for payload builders (Discord shape, generic canonicalization, HMAC determinism).
- Integration test: round-trip a generic webhook against a stubbed receiver that verifies the signature.
- Integration test: Discord embed against `https://discord.com/api/webhooks/.../test-only` (or recorded fixture) — manual verification only since Discord doesn't offer a sandbox.
- Concurrency test: 100 deliveries in parallel; verify retry rows aren't double-counted.
- Auto-disable test: simulate 3 consecutive deliveries returning 404 and verify the webhook is disabled with the correct in-app notification; verify 1-2 isolated 4xx blips do **not** disable it; verify persistent timeouts exhaust the 10-attempt schedule before disabling.
- Outbox recovery test: commit deliveries with `pending` attempt rows, skip the inline dispatcher, and verify the retry worker sends them after the 60s recovery window.
- Manual verification:
  - Create a Discord webhook URL.
  - Add it to a profile.
  - Trigger a release event.
  - Confirm the embed renders correctly in Discord.
  - Disable the Discord webhook on Discord's side.
  - Trigger another event.
  - Verify the webhook auto-disables after retries.
  - Verify the profile receives an in-app "webhook auto-disabled" notification.

## Open Questions

- **Discord image proxy scope.** Build it in v1, or ship v1 without images and add v1.5? Recommendation: ship without images in v1 — text-only embeds are useful and require no new infra. Add the proxy in v1.5 if user feedback wants images.
- **Slack and Microsoft Teams native types.** Should we add `slack` and `teams` types alongside `discord` and `generic`? Both Slack incoming webhooks and Teams Adaptive Cards have their own JSON shapes. Recommendation: `generic` works for Slack (Slack incoming webhooks accept arbitrary JSON; users can build their own message format with a Workflow). Teams requires Adaptive Card JSON specifically. Defer both to v2 unless concrete user demand emerges.
- **Per-event-type templating.** Some users will want to customize the embed text. Recommendation: defer to v2; v1 fixed shapes ship faster and most users will accept them.
- **Webhook "channels" beyond episode.available.** Should webhook payloads cover other event types (release-aggregated, server announcements)? V1 only fires on `notification.created` for `episode.available`. Other types are forward-compatible via the `event` and `type` fields.
- **Outgoing IP.** Webhook deliveries originate from the user's Silo server IP. Do we need an option to route through a proxy? Recommendation: not v1; document that deliveries come from the server's egress IP.
- **Backpressure.** What happens when a profile's webhook is slow but reachable, blocking delivery for an hour at a time? The 10s timeout caps individual deliveries; bounded worker pool caps fanout concurrency. A single bad webhook shouldn't degrade other webhooks because each delivery runs independently.
