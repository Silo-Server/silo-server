# Silo Notifications — Design Index

> **Provenance:** Imported 2026-06-11 from `ContinuumApp/continuum` (`docs/superpowers/plans/notifications/` at `04aa9266`), written 2026-04-28 before the project was renamed to Silo. Reviewed and amended 2026-06-11: product naming and all wire contracts (payload keys, headers, settings enums, localization keys, provisional domains/repo names) are normalized to Silo. Bundle topics, Android package names, and Firebase project IDs intentionally keep their current official client-build values (`com.continuum.app.*`, `continuum-prod-android`) — they are allowlist *config*, not part of this design's contract, and the shipped client builds still use them. Relay/proxy hostnames (`relay.silo.app`, `media.discord-cdn-proxy.silo.app`) are provisional. See "2026-06-11 review amendments" below for the design-level changes made during review.

This folder collects the design work for Silo's notification system, covering durable in-app inbox + websocket realtime, Apple Push (APNs), Android Push (FCM), and outbound webhooks (Discord-native + generic).

A self-contained visual summary of the design decisions (who/what/where/why per decision, pipeline, trust model) lives at [`design-decisions.html`](./design-decisions.html) — open it in any browser; no build or network access required.

## Reading order

1. **[`00-architecture-overview.md`](./00-architecture-overview.md)** — Cross-cutting overview. Read this first. Explains the channel model, fanout pipeline, preference shape, threat model, and addressing model (why a relay is required for mobile push). Links into the per-channel specs.
2. **[`01-release-events-and-inbox.md`](./01-release-events-and-inbox.md)** — Foundation. Durable `release_events` -> per-profile `notification_deliveries` fanout, the in-app inbox API, the websocket channel, and per-profile preferences. Vendor-agnostic; nothing else works without this.
3. **[`02-apns-relay.md`](./02-apns-relay.md)** — Apple push. Privacy-preserving design with three provider modes (`off` / `silo_relay` / `custom_apns`).
4. **[`03-fcm-relay.md`](./03-fcm-relay.md)** — Android push. Mirrors the APNs spec with `off` / `silo_relay` / `custom_fcm` modes using FCM v1 (OAuth2 service-account JSON).
5. **[`04-outbound-webhooks.md`](./04-outbound-webhooks.md)** — Profile-scoped outbound webhooks. Native Discord embed type and generic JSON+HMAC type. Content included by default because the profile chose the destination.

## Status

| Doc | Status |
|---|---|
| 00-architecture-overview | Draft (amended 2026-06-11) |
| 01-release-events-and-inbox | Draft (refined, amended 2026-06-11) |
| 02-apns-relay | Draft (refined, amended 2026-06-11) |
| 03-fcm-relay | Draft (amended 2026-06-11) |
| 04-outbound-webhooks | Draft (amended 2026-06-11) |

Nothing in this folder has been implemented. The codebase has zero notification-related code beyond the existing realtime events hub (`internal/events/`, with the operational publisher wrapper at `internal/notifications/hub.go`), which publishes catalog/jobs events and is unrelated to the user-facing notification system designed here.

## 2026-06-11 review amendments

A scaling and privacy review (self-hosted servers with hundreds of users) resolved four design-level gaps. The detailed designs live in the per-doc sections; this is the index:

1. **Back-catalog seeding + burst suppression** (`01`). First scan of a new library and the feature-enable backfill seed `episode_availability` *without* creating release events; bulk additions to existing libraries are bounded by a per-series fanout burst cap. Without this, importing a 200-episode back-catalog of a popular series would generate tens of thousands of deliveries and pushes in one scan.
2. **Durable dispatch outbox** (`01`, referenced by `02`/`03`/`04`). The fanout transaction that inserts `notification_deliveries` also inserts `pending` per-target attempt rows for the push and webhook channels. A crash between delivery commit and dispatch no longer silently loses pushes/webhooks; recovery workers drain stale pending rows.
3. **Cross-library episode dedupe** (`01`). Media items in Silo are catalog-level (`media_item_libraries` junction), so the same episode landing in "TV" and "TV 4K" shares one `episode_id`. A partial unique index on `(profile_id, episode_id)` guarantees at most one `episode.available` delivery per profile per episode across libraries.
4. **Mobile wake-fetch endpoints defined in the foundation** (`01`). `GET /api/v1/notifications/sync` (forward cursor) and `GET /api/v1/notifications/{id}` — previously referenced by `02`/`03` but defined nowhere.

Smaller amendments: relay threat models now list the server's egress IP as residual leakage (`00`/`02`/`03`); `collapse_id`/`collapse_key` derivation is specified as per-server-keyed HMAC so the relay can't read series identity out of it (`02`/`03`); the websocket handshake uses a short-lived single-use ticket instead of a long-lived profile token in the query string (`00`/`01`); client-side relay pacing (`02`/`03`); webhook auto-disable requires 3 consecutive non-retryable 4xx instead of 1 (`04`); SSRF deny list gains `198.18.0.0/15` and `192.88.99.0/24` (`04`); interest recompute fires on watch-state transitions, not every progress tick (`01`); profile-deletion cascades and `release_events.dedupe_key` composition are specified (`01`); migration tasks now follow the repo's timestamped Goose convention (`01`).

## Scope summary

**In scope (v1):**

- Episode-availability notifications (a new episode lands in a library and is relevant to a profile via favorites / watchlist / continue-watching / next-up).
- Durable per-profile inbox with read/unread state.
- Realtime websocket delivery for connected clients.
- Apple Push via opt-in Silo-operated relay or admin-supplied custom APNs credentials.
- Android Push via opt-in Silo-operated relay or admin-supplied custom FCM credentials.
- Outbound webhooks per profile, with native Discord embed rendering and generic JSON+HMAC.

**Out of scope (v1):**

- Movie release notifications (the trigger surface — favoriting a movie that doesn't yet exist — isn't a flow Silo supports).
- Email, SMS, or other delivery channels.
- Browser Web Push (PWA) notifications.
- Cross-profile administrator dashboards.
- Marketing or promotional notifications.
- Third-party ingest events (Plex/Sonarr/Radarr) as notification sources.

## Cross-cutting decisions

These hold across all channels and were resolved during spec review:

- **Episode-only triggers in v1.** Confirmed: there's no UX path for a user to favorite/watchlist a movie before it exists in their library.
- **Per-profile webhooks include content by default.** The profile chose the destination; trust is implicit. Discord-native embeds show series and episode details; generic webhooks include the same.
- **Mobile push payloads stay opaque by default.** APNs and FCM relay paths never see notification content. The app fetches metadata from the user's own server after wake. No badge counts in v1 (deferred to user-opt-in in v2).
- **Preference shape is flat:** per-profile reason flags (favorites / watchlist / continue-watching / next-up) plus a master enable toggle on each push device and each webhook. Not a per-channel × per-reason matrix.
- **Per-platform APNs topics.** iOS, tvOS, and macOS each get their own bundle topic. Single-build collapse is a v2 concern.
- **Stateless relay v1.** No stored token aliases. Add only if abuse/rate-limit pressure justifies it.
- **Relay services live in their own repos.** This folder defines the contracts the user-server side implements; the actual relay deployment (APNs / FCM) is a separate operational repo.
- **Back-catalog never floods.** Initial library scans and the feature-enable backfill seed availability silently; per-series burst caps bound fanout for bulk additions (see `01`).
- **One delivery per episode per profile, across libraries.** Dual-quality library setups (e.g., "TV" + "TV 4K") do not double-notify.
- **Dispatch enqueue is durable (outbox).** Push and webhook sends survive a crash between delivery commit and dispatch; the websocket channel stays best-effort because the inbox snapshot covers reconnect.

## Phasing

Implementation should land in this order, gated by feature flags so partial deploys are safe:

1. Schema (Phase 1 of `01-release-events-and-inbox.md`).
2. `profile_series_interest` updaters and backfill task (no fanout yet).
3. Availability seeding backfill, then release event creation (still no fanout). Seeding **must** complete before release events are enabled, or the first scan after enablement emits an event per back-catalog episode.
4. In-app fanout worker, websocket channel, inbox APIs.
5. Frontend inbox + badge + preference UI.
6. Outbound webhooks (no new infra; can ship before mobile push).
7. APNs relay + custom APNs.
8. FCM relay + custom FCM.

The relay services (steps 7-8) require Silo to provision Apple Developer + Firebase developer accounts and host the relay services. Steps 1-6 require no new external infrastructure.
