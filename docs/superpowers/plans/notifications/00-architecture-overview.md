# Silo Notifications — Architecture Overview

**Date:** 2026-04-28
**Status:** Draft
**Scope:** Cross-cutting design covering all delivery channels: durable in-app inbox, realtime websocket, Apple Push (APNs), Android Push (FCM), and outbound webhooks.
**Companion docs:**
- [`01-release-events-and-inbox.md`](./01-release-events-and-inbox.md) — Foundation
- [`02-apns-relay.md`](./02-apns-relay.md) — Apple push
- [`03-fcm-relay.md`](./03-fcm-relay.md) — Android push
- [`04-outbound-webhooks.md`](./04-outbound-webhooks.md) — Discord and generic webhooks

## Goal

Silo should notify a profile when a new episode of a series they care about becomes available in their library. "Care about" means at least one of: the series is favorited, watchlisted, in continue-watching, or has a profile-eligible next-up cursor.

The notification should reach the profile through whichever delivery channels the profile has configured: an in-app inbox with realtime push to connected clients, mobile push notifications to Apple and Android devices, and outbound webhooks to user-chosen destinations like a Discord channel.

## Why this is hard

Three constraints pull in different directions:

1. **Self-hosted privacy.** Silo is self-hosted media. Notification content (episode titles, posters, profile names, library identity) must never leave the user's server unless the user has explicitly opted into a destination that requires it.
2. **Mobile push gatekeeping.** Apple and Google require pushes to the official App Store / Play Store builds to be authenticated by the credentials of the developer account that signed and published the app. There is no fully self-hosted, zero-Silo-infrastructure path to push to the official mobile apps.
3. **Multi-profile fairness.** A single user account on a Silo server can have multiple profiles (kid, adult, parent, etc.). Notifications must be scoped to the active profile. Devices, push tokens, webhooks, and preferences are all profile-scoped.

The design resolves these by:

- Routing all notification work through a durable per-profile inbox that lives on the user's own server. Everything else is a transport on top of this inbox.
- Operating a privacy-preserving relay for APNs and FCM that holds the official-app credentials and accepts only opaque push requests. Self-hosted servers opt in. The relay never sees notification content.
- Profile-scoping every database row, every API surface, every websocket subscription, and every push registration.

## Mental model

```
                    ┌─────────────────────────────────────────────────┐
                    │                                                 │
   ingest/scan ────► release_events  (one row per newly-available     │
                    │   episode in a library)                         │
                    │      │                                          │
                    │      ▼                                          │
                    │   fanout worker                                 │
                    │      │                                          │
                    │      │   loads profile_series_interest          │
                    │      │   applies eligibility + preferences      │
                    │      ▼                                          │
                    │   notification_deliveries  (one row per         │
                    │      eligible profile per release)              │
                    │      │                                          │
                    │      │   AFTER COMMIT                           │
                    │      ▼                                          │
                    │   channel dispatcher  ◄── fans out to channels: │
                    │                                                 │
                    │   ┌───────────────┬───────────────┬─────────┐   │
                    │   │               │               │         │   │
                    │   ▼               ▼               ▼         ▼   │
                    │ websocket    apple push       android   webhooks│
                    │ (in-app)     (APNs relay      push      (per    │
                    │              or custom)       (FCM)     profile)│
                    │   │               │               │         │   │
                    └───┼───────────────┼───────────────┼─────────┼───┘
                        ▼               ▼               ▼         ▼
                 connected       iOS / tvOS /        Android    Discord /
                 browser tabs    macOS apps          apps       user URL
                 and apps        (wake then          (wake)     (content
                                 fetch metadata)                included)
```

Three invariants make this work:

- **The durable row is the source of truth.** Realtime websocket events, mobile pushes, and webhooks are all triggered *after* the `notification_deliveries` row commits. If any transport fails, the inbox row still exists and shows up on next reconnect or refresh.
- **The fanout worker decides recipients exactly once.** Recipients are derived from a compact `profile_series_interest` index keyed by `(library_id, series_id)`. Per-channel transports are purely about delivery — they never make recipient decisions.
- **Dispatch enqueue is durable (outbox).** The same fanout transaction that inserts `notification_deliveries` also inserts `pending` per-target attempt rows for the push and webhook channels (`push_delivery_attempts` / `webhook_delivery_attempts`). The post-commit dispatcher *claims* those rows for immediate delivery; recovery workers sweep stale `pending` rows. A crash between delivery commit and dispatch therefore delays pushes/webhooks instead of silently dropping them. The websocket channel deliberately has no outbox row — it is best-effort because the inbox snapshot covers reconnect. See `01-release-events-and-inbox.md` "Transaction and Concurrency Rules".

## Channels at a glance

| Channel | Where it lives | Trust model | Content visibility |
|---|---|---|---|
| **In-app inbox** | User's own server, accessed via authenticated REST API | Same auth as the rest of Silo | Full content |
| **Realtime websocket** | User's own server, `/events/ws` channel `notifications` | Same auth, profile-scoped | Full content |
| **Apple Push (APNs)** | Silo-operated relay (or admin-supplied custom APNs credentials) | Opt-in. Self-hosters trust the relay to forward opaque requests. | Opaque payload only — app fetches content from user server after wake |
| **Android Push (FCM)** | Silo-operated relay (or admin-supplied custom FCM credentials) | Same as APNs | Opaque data-only message — app fetches content from user server after wake |
| **Outbound webhooks** | User-chosen destination URL (e.g., Discord, Slack, custom service) | Profile chose the destination; trust is implicit | Full content included by default. Discord type renders native embeds; generic type sends signed JSON |

## Triggers (what generates a notification)

A notification is generated when a new episode becomes available in a library and at least one profile has interest in the series:

- **Favorite** — the series is favorited by the profile.
- **Watchlist** — the series is on the profile's watchlist.
- **Continue watching** — the profile has in-progress watch state for the series.
- **Next up** — the new episode is at or beyond the profile's `next_expected_episode_key` cursor and the profile has progression state for the series.

A profile may match multiple reasons for a single release event. The `notification_deliveries` row records all matching reasons in a `reason_flags` JSONB column; the fanout produces exactly one delivery per `(profile_id, release_event_id)` regardless of how many reasons matched. A second uniqueness rule spans libraries: because Silo media items are catalog-level (`media_item_libraries` is a junction), the same episode landing in two libraries (e.g., "TV" and "TV 4K") shares one `episode_id`, and at most one `episode.available` delivery is created per `(profile_id, episode_id)` — dual-quality library setups do not double-notify.

**Back-catalog protection:** "newly available" must mean *newly released to this server*, not *newly seen by the notifications feature*. The first scan of a newly created library and the one-time feature-enable backfill **seed** `episode_availability` without creating release events, and the fanout worker applies a per-series burst cap for bulk additions to existing libraries. Without this, importing a 200-episode back-catalog of a series with hundreds of interested profiles would generate tens of thousands of deliveries and pushes in a single scan. Full rules in `01-release-events-and-inbox.md` "Seeding and Burst Suppression".

**v1 explicitly does not support:**

- Movie additions. Silo has no movie-availability detector parallel to the per-episode one in this design; without it, there's nothing to fan out from for movies. Users *can* watchlist movies that don't yet exist in their library (via TMDB lookups), but the missing piece is the ingest-time signal that a watchlisted movie has now arrived. v2 can add a `movie_availability` table and analogous release events; the fanout/inbox/channel layers in this design are movie-ready by construction.
- Metadata-only updates (poster refreshes, title corrections).
- Generic library-changed notifications.
- Aggregated notifications like "3 new episodes available." Per-episode deliveries first; aggregation is a presentation concern that can stack on top.

## Mode terminology

The design uses three vocabularies that correspond to different layers; they are intentionally not the same word. This is the canonical mapping:

| Layer | Terms | Where it lives |
|---|---|---|
| **Profile-level push mode** (UI-facing) | `off`, `in_app_only`, `private_push` | `push_devices.push_mode` |
| **APNs wire mode** (relay request `mode` field) | `private_alert`, `background_wake` | Sent to APNs relay or used in custom APNs payload builder |
| **FCM wire mode** (relay request `mode` field) | `private_data`, `background_wake` | Sent to FCM relay or used in custom FCM payload builder |

Mapping:

- Profile `private_push` → APNs `private_alert` for visible wakes, FCM `private_data` for visible wakes.
- Profile `in_app_only` → no remote push at all (websocket / inbox only).
- Profile `off` → no notifications for this device whatsoever.
- The `background_wake` wire mode is reserved for low-noise sync (per-channel spec details). It is not exposed at profile level in v1.

## Preference model

Preferences are profile-scoped and flat. There is no per-channel × per-reason matrix.

```
notification_preferences (per profile)
├── enabled                  bool   — global kill switch
├── notify_favorites         bool
├── notify_watchlist         bool
├── notify_continue_watching bool
└── notify_next_up           bool
```

Per-webhook reason gating uses **the same four boolean columns** on `notification_webhooks` (`notify_favorites`, `notify_watchlist`, `notify_continue_watching`, `notify_next_up`) — not a single structured `reason_filters` JSON field.

**Precedence:** profile preferences are a **hard gate** applied during fanout (a delivery row is not created if all matching reasons are disabled at the profile level). Per-webhook flags and per-device push mode are **additional filters** applied during dispatch — they can narrow what fires for a specific destination but cannot re-enable a reason the profile has globally disabled.

Each push device has a `push_mode` (`off`, `in_app_only`, `private_push`) that gates whether mobile push fires for that specific device. Each webhook has its own `enabled` flag and per-webhook `notify_favorites` / `notify_watchlist` / `notify_continue_watching` / `notify_next_up` booleans for finer control.

The fanout worker uses `notification_preferences` to decide whether to *create* a `notification_delivery` at all. The channel dispatcher uses per-device and per-webhook flags to decide which transports fire after the row commits.

This means: if a profile turns off `notify_continue_watching`, a delivery is never created for a continue-watching-only match, and no transports fire. If a profile leaves the reason flags on but disables a specific Discord webhook, the inbox row + websocket + push still fire normally — the webhook just doesn't.

## Mobile push: why a relay is necessary

Apple and Google operate the only push infrastructure that can wake a closed app on iOS and Android. To send a push to the official Silo app on the App Store or Play Store, the request must be authenticated by the credentials of the developer account that signed and published that app build:

- **APNs:** Apple Developer Team ID + Auth Key ID + `.p8` private key. The auth key is account-scoped (not topic-scoped); it can sign JWTs that authorize push to any bundle topic the team owns. The `apns-topic` request header selects which topic the push targets, and Apple rejects requests whose topic is not owned by the signing team.
- **FCM:** Firebase service account JSON whose private key signs a JWT exchanged for an OAuth2 access token. The service account is scoped to a specific Firebase project, and the project owns one or more registered Android app package names. (Package + SHA-1 cert fingerprint registration matters for Firebase Authentication and App Check, but for plain FCM v1 push the service account + package name + matching FCM registration token are what's required.)

Distributing these credentials to thousands of self-hosted Silo installations is not viable:

1. It violates Apple's and Google's developer agreements, which prohibit sharing credentials outside the team.
2. Anyone with a copy could push to every Silo app worldwide.
3. Apple actively monitors and revokes leaked keys.

Therefore the design separates two paths:

### Hosted relay (`silo_relay`) — the default for official-app users

A small Silo-operated service holds the official APNs `.p8` key and FCM service account JSON. Self-hosted servers opt in by configuring a relay API key, then call the relay with opaque push requests. The relay signs and forwards to APNs / FCM. The relay never sees notification content.

The relay is **stateless on the request path**. Its database holds only:

- relay accounts (one per opt-in self-hosted server installation)
- relay API keys (with `last_used_at`, revocable)
- minimal per-account allowlists (which app topics this account may push to)
- redacted operational logs (no APNs/FCM tokens, no notification content)

It does **not** store user identities, profile data, device-to-user mappings, or notification content. Apple and Google do the actual addressing — when a push request goes out, the relay just forwards `{token, opaque_ids}` to APNs/FCM, and APNs/FCM deliver based on the device token.

### Custom credentials (`custom_apns` / `custom_fcm`) — escape hatch

Power users, white-label deployments, or anyone running a fork that ships its own resigned mobile app can configure their own Apple/Google developer credentials. The user's Silo server signs requests directly using those credentials and sends to APNs/FCM without going through the Silo relay.

Custom mode is **only useful if the admin has shipped a custom-signed app build under their own developer account** with their own bundle/package ID. It does not let a self-hoster push to the official App Store / Play Store app — that's not technically possible without the official developer credentials.

### Addressing flow walkthrough

The most common question on this design is "how does the relay know which user/device to send to with thousands of servers and thousands of users?" The answer is: it doesn't, because Apple and Google handle addressing.

```
1. User installs official Silo app on iPhone from App Store.
2. App calls iOS push registration; Apple returns an opaque APNs device token
   (~100 bytes, unique per app install per device).
3. App POSTs that token to the user's OWN server:
     POST https://my-silo.example.com/api/v1/devices/push/apple
     { device_id, apns_token, ... }
4. User's server stores in its push_devices table, scoped to the active profile.
5. New episode lands. Fanout worker creates a notification_delivery row.
6. Channel dispatcher loads the profile's enabled push_devices and POSTs
   to the relay (or signs directly via custom_apns):
     POST https://relay.silo.app/v1/apple/send
     Authorization: Bearer rk_thisServersRelayApiKey
     {
       "token": "740f...",                    ← APNs device token from step 3
       "topic": "com.continuum.app.ios",
       "environment": "production",
       "mode": "private_alert",
       "server_device_id": "01JOPAQUE...",    ← lets app find right local server
       "delivery_id": "01JDELIVERY..."        ← lets app fetch notification meta
     }
7. Relay signs JWT with Silo's .p8, forwards to APNs:
     POST https://api.push.apple.com/3/device/740f...
     apns-topic: com.continuum.app.ios
     authorization: bearer <signed JWT>
     {generic opaque payload with silo.* fields}
8. Apple delivers to the iPhone holding token 740f...
9. App wakes, reads server_device_id, finds matching local server account,
   GETs full notification content from the user's own server.
```

The relay knows: a relay API key was used (so: which self-hosted server installation), the server's egress IP address (inherent to any hosted relay — the server connects to it directly), an APNs device token, a timestamp, and opaque IDs. The relay does not know: which user, which profile, what notification content, what server URL, what library, or what media item. Even a fully compromised relay leaks only timing + device tokens + egress IPs, not notification content or identity. The egress IP is the closest thing to an identity leak in this design — for home hosting it identifies the household's connection — and is listed here so the privacy claims stay exhaustive rather than over-strong.

## Threat model summary

Detailed per-channel threat models live in each channel's spec. The cross-cutting summary:

| Channel | Adversary | Worst case |
|---|---|---|
| In-app inbox + websocket | Network attacker | Existing Silo auth threat model. Notifications add no new surface. |
| APNs / FCM relay (operator) | Compromised relay or hostile relay operator | Can learn device tokens, push timing, and the server's egress IP for opted-in servers. Cannot learn user identity, notification content, server URL, or library data. Cannot fabricate meaningful pushes (only generic opaque payloads). |
| APNs / FCM (Apple / Google) | Apple / Google as platform operator | Can see app topic, device token, generic payload, timing. No user identity, no media metadata, no server URL. |
| Outbound webhooks | Webhook destination operator (e.g., Discord) | Sees full notification content (titles, posters, episode info) for the profile that configured the webhook. The profile chose this destination; this is the explicit trade. |
| Outbound webhooks | Network attacker who owns a URL submitted by the user | Can receive the profile's notifications until the webhook is removed. Mitigated by HMAC for generic webhooks, HTTPS-only, host blocklist (no localhost / RFC1918 by default), and a profile-visible "last seen" / failure status. |

## Channel dispatcher boundary

A single internal `Dispatcher` interface fans the durable row out to channels. Each channel implements the same minimal contract:

```go
type Dispatcher interface {
    // Called once per notification_deliveries row, AFTER the row commits.
    // Per-target dispatchers (push, webhooks) do not decide their own work:
    // the fanout transaction already enqueued one `pending` attempt row per
    // (delivery_id, target_id), where target_id is push_device.id or
    // notification_webhook.id. Dispatch claims and sends those rows; a
    // recovery worker sweeps `pending` rows whose claim never happened
    // (crash between commit and dispatch). Idempotency is therefore
    // by (delivery_id, target_id, attempt_number). The websocket dispatcher
    // has no per-target row and is instead idempotent by delivery_id alone —
    // re-publishing the same delivery_id is a no-op for connected clients.
    Dispatch(ctx context.Context, delivery NotificationDelivery) error
}
```

Implementations:

- `WebsocketDispatcher` — publishes a `notification.created` event on the `notifications` channel, scoped to the delivery's `(user_id, profile_id)`. Best-effort; durable row is the source of truth.
- `ApplePushDispatcher` — selects enabled `push_devices` for the profile where `platform = 'apple'` and routes through the configured provider (`silo_relay` or `custom_apns`). Records each attempt in `push_delivery_attempts`.
- `AndroidPushDispatcher` — same but `platform = 'android'`, routes through `silo_relay` or `custom_fcm`.
- `WebhookDispatcher` — selects enabled `notification_webhooks` for the profile, applies per-webhook reason filters, and POSTs the channel-specific payload (Discord embed or generic JSON+HMAC). Records each attempt in `webhook_delivery_attempts`.

Channel failures are isolated: if APNs is down, webhooks and websocket still fire. If a webhook destination is unreachable, push still fires. The fanout worker does **not** wait for any dispatcher to succeed before marking the release event processed; the durable delivery rows plus the `pending` outbox attempt rows (enqueued in the same transaction) guarantee eventual delivery for push and webhooks even across a crash. The relay-bound dispatchers additionally pace their sends client-side (token bucket) so a burst never trips the relay's rate limits — see `02`/`03` "Relay pacing".

## Capability surface

The frontend and mobile clients need to know what's available without guessing. A single capability endpoint returns the truth:

```http
GET /api/v1/notifications/capability
```

```json
{
  "in_app": { "enabled": true },
  "apple_push": {
    "available": true,
    "provider": "silo_relay",
    "supported_modes": ["private_push", "in_app_only"]
  },
  "android_push": {
    "available": false,
    "provider": "off",
    "supported_modes": ["in_app_only"]
  },
  "webhooks": {
    "available": true,
    "max_per_profile": 10,
    "supported_types": ["discord", "generic"]
  }
}
```

Clients render setup UI from this response. They never have to introspect admin settings.

## Data model overview

The full schemas live in the per-channel specs. The shared shape:

| Table | Purpose | Spec |
|---|---|---|
| `episode_availability` | "Episode E became available in library L." Idempotent. Seeded silently for back-catalog. | 01 |
| `release_events` | One per `(library_id, episode_id)` newly-available event. | 01 |
| `notification_library_seed_state` | Per-library marker: availability has been seeded; release events may now be emitted. | 01 |
| `profile_series_interest` | Compact recipient index keyed by `(profile_id, library_id, series_id)`. | 01 |
| `notification_deliveries` | Per-profile durable inbox row. Cross-library dedupe via partial unique `(profile_id, episode_id)`. | 01 |
| `notification_preferences` | Per-profile reason toggles + master enable. | 01 |
| `push_devices` | Per-profile-per-device push registration. Platform-tagged (`apple` / `android`). | 02 / 03 |
| `push_delivery_attempts` | Per-attempt push log. No notification content. | 02 / 03 |
| `notification_webhooks` | Per-profile webhook destinations (Discord or generic). | 04 |
| `webhook_delivery_attempts` | Per-attempt webhook log. | 04 |

A single migration block lands the foundation tables. APNs/FCM/webhook tables can land in separate migrations as those channels ship — they don't block the v1 in-app inbox.

## API surface overview

Profile-scoped (require `X-Profile-Id` header):

- `GET /api/v1/notifications` — paginated inbox (newest-first, `before` cursor).
- `GET /api/v1/notifications/sync` — forward sync (`since` cursor); the endpoint mobile clients call after a push wake.
- `GET /api/v1/notifications/{id}` — single delivery by ID.
- `GET /api/v1/notifications/unread-count` — for the badge.
- `POST /api/v1/notifications/{id}/read` — mark single read.
- `POST /api/v1/notifications/read-all` — mark all read.
- `POST /api/v1/events/ws-ticket` — mint a short-lived single-use websocket ticket bound to `(user_id, profile_id)`.
- `GET /api/v1/notifications/preferences` — read profile prefs.
- `PUT /api/v1/notifications/preferences` — update profile prefs.
- `GET /api/v1/notifications/capability` — what channels are available.
- `POST /api/v1/devices/push/apple` — register an APNs device.
- `POST /api/v1/devices/push/fcm` — register an FCM device.
- `DELETE /api/v1/devices/push/{id}` — disable a push device.
- `GET /api/v1/notifications/webhooks` — list profile's webhooks.
- `POST /api/v1/notifications/webhooks` — create a webhook.
- `PUT /api/v1/notifications/webhooks/{id}` — update a webhook.
- `DELETE /api/v1/notifications/webhooks/{id}` — delete a webhook.
- `POST /api/v1/notifications/webhooks/{id}/test` — fire a test event.

Admin-scoped:

- Settings registry entries for APNs and FCM provider configuration (admin-only via existing `internal/api/handlers/settings.go` patterns).

## Realtime channel

A new event channel `notifications` joins the existing `catalog`, `jobs`, `sessions`, `tasks`, `scans`, `history_import`, `user_state`, `plugins` channels in `internal/events/types.go`. The websocket handler must validate active profile identity and must reject `notifications` events whose `profile_id` doesn't match the connection's bound profile.

Because browsers can't set custom headers on WebSocket connections, profile identity is carried by a **short-lived single-use ticket**: the client calls `POST /api/v1/events/ws-ticket` (normal auth + `X-Profile-Id`), receives an opaque ticket valid for ~30 seconds, and passes it as a `ticket` query parameter on the handshake. The server consumes the ticket and binds the connection to the `(user_id, profile_id)` it was minted for. A long-lived profile token must **not** be passed in the query string — self-hosted servers commonly sit behind reverse proxies whose access logs capture query strings, and a logged ticket that expired 30 seconds after minting is harmless where a logged profile token is not.

Event types on this channel:

- `notification.created` — a new delivery for the bound profile.
- `notification.read` — a delivery was marked read (allows multi-tab coherence).

Snapshot on subscribe: recent unread deliveries for the bound profile, so reconnecting clients hydrate without a separate REST call.

## Out of scope for v1

- Movie release notifications (no trigger surface; v2).
- Request-fulfilled notifications ("your requested item is now available"). Silo's requests system postdates this design; it is the most obvious next notification type and slots into the extensible `notification_deliveries.type` registry without schema changes. v2.
- Aggregated notifications ("3 new episodes available"; presentation layer, can stack on per-episode rows later). The per-series burst cap in `01` bounds bulk-addition volume until an aggregated delivery type exists.
- Web Push (browser PWA notifications via Push API). Possible v2 — would mirror APNs/FCM as a third push platform.
- Email or SMS delivery.
- Cross-profile / household-wide notification views.
- Admin-level system notifications ("scan complete", "library health alert"). The existing realtime events hub (`internal/events/`, with the operational publisher wrapper at `internal/notifications/hub.go`) already handles operational events; this folder's design is strictly user-facing media notifications.
- Notification templates / customization beyond Discord-vs-generic.
- Time-window quiet hours. Could be added to `notification_preferences` later; not v1.
- Locale negotiation for push payloads. APNs / FCM payloads use generic localization keys (`SILO_NOTIFICATION_GENERIC_BODY` etc.) so the OS picks language from device locale.

## Open questions

These are the few remaining items not yet decided. None block writing the per-channel specs.

1. **Service-level rate limits per relay account.** Per-second and per-day caps need real numbers. Suggest 10 req/sec burst, 50,000 req/day initial quota; tune from logs. The server-side dispatchers pace their relay calls client-side (default 5 req/sec token bucket, see `02`/`03`) so a hundreds-of-users server drains bursts smoothly instead of slamming into 429s; the per-series burst cap in `01` bounds worst-case burst size.
2. **Webhook destination domain blocklist edge cases.** RFC ranges are defined in `04-outbound-webhooks.md`; remaining open question is whether IPv6 v4-mapped addresses (`::ffff:0:0/96`) and CGNAT (`100.64.0.0/10`) need any admin escape hatch beyond the global `allow_private_destinations` flag.
3. **Discord image proxy timing.** Ship in v1 (build `media.discord-cdn-proxy.silo.app`) or defer to v1.5 with text-only embeds? Recommendation in `04`: defer to v1.5; v1 ships text-only embeds.

Resolved during review (no longer open):

- **Relay service code lives in a separate repo** (provisional `silo-push-relay`). This folder defines the contract.
- **Migration ordering.** Foundation schema lands as one migration; per-channel schemas can land separately as their channels ship.
- **`server_device_id` lifecycle.** Stable across token rotation; rotates only when a device is removed and re-registered.

## Phasing

Implementation phases gated by feature flags:

| Phase | Flag | Capability |
|---|---|---|
| 1 | `notifications.schema_enabled` | Tables exist; no writes. |
| 2 | `notifications.interest_updaters_enabled` | `profile_series_interest` updates land on favorites/watchlist/playback events. Backfill task runs. |
| 3 | `notifications.release_events_enabled` | Availability seeding backfill runs first, then ingest writes `release_events`. No fanout yet. |
| 4 | `notifications.fanout_enabled` | Fanout worker runs; `notification_deliveries` rows materialize. Websocket channel published. |
| 5 | `notifications.ui_enabled` | Inbox API + frontend badge + page exposed. |
| 6 | `notifications.webhooks_enabled` | Outbound webhooks + Discord/generic dispatchers. v1 ships **text-only** Discord embeds (no `image` or `avatar_url` fields) to avoid leaking the user's server origin to Discord. v1.5 adds the optional `media.discord-cdn-proxy.silo.app` proxy and re-enables embed images. |
| 7 | `notifications.apple_push_enabled` | APNs registration + dispatcher. Requires relay infra (or admin-supplied custom credentials). |
| 8 | `notifications.android_push_enabled` | FCM registration + dispatcher. Requires relay infra (or admin-supplied custom credentials). |

Phases 1-6 ship without any new external infrastructure. Phases 7-8 require Silo to provision Apple Developer + Firebase developer accounts and operate the relay service.
