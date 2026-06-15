# Web Push Spec

**Date:** 2026-06-11
**Status:** Implemented
**Scope:** Browser push notifications (Push API + VAPID) as a third push platform alongside the deferred APNs/FCM channels.
**Depends On:**
- [`00-architecture-overview.md`](./00-architecture-overview.md)
- [`01-release-events-and-inbox.md`](./01-release-events-and-inbox.md)

## Why Web Push ships before APNs/FCM

The architecture overview deferred mobile push because Apple and Google require pushes to official store builds to be signed by the publisher's credentials, forcing a Silo-operated relay. Web Push has neither problem:

- **No accounts, no relay.** The server self-provisions a VAPID keypair on first use. Any standards-compliant browser push service (Chrome, Firefox, Edge, Safari 16+) accepts VAPID-signed requests from any origin.
- **Content-safe by protocol.** Payloads are encrypted end-to-end (RFC 8291, `aes128gcm`) to keys held only by the subscribed browser. The vendor push service relays ciphertext. Unlike the APNs/FCM design, payloads can therefore carry full display content (titles, episode numbers, poster URLs) without violating the self-hosted privacy model — there is no opaque-wake/fetch dance.

The residual leak matches the relay threat model: the push service sees the user server's egress IP, delivery timing, and payload size. It never sees content or identity.

## Data model

- `web_push_subscriptions` — profile-scoped browser registrations: `endpoint` (unique; a resubscription from the same browser under a different profile reassigns the row), `p256dh`, `auth`, `device_name`, failure bookkeeping. No FK to profiles (per-user SQLite stores); profile deletion purges in code.
- `web_push_delivery_attempts` — the durable dispatch outbox, mirroring `webhook_delivery_attempts`: `pending` rows enqueued in the fanout transaction, claimed post-commit with a lease, swept by the retry loop after a crash.

## VAPID identity

Generated once and persisted in `server_settings`:

- `notifications.web_push.vapid_public_key` — served to clients via the capability endpoint.
- `notifications.web_push.vapid_private_key` — encrypted at rest (`SensitiveSettingKeys`).

The private key is persisted before the public key so a crash between writes regenerates the pair instead of stranding clients with an unusable public key. The pair must never be rotated casually: browsers bind subscriptions to it.

## API surface (profile-scoped)

- `GET /api/v1/notifications/capability` — `web_push: { available, public_key }`.
- `POST /api/v1/notifications/web-push/subscriptions` — body is `PushSubscription.toJSON()` plus `device_name`.
- `GET /api/v1/notifications/web-push/subscriptions` — for the settings UI device list.
- `DELETE /api/v1/notifications/web-push/subscriptions/{id}`
- `POST /api/v1/notifications/web-push/unsubscribe` — by endpoint (browsers don't know row IDs).

Subscription endpoints are attacker-controllable URLs the server will POST to, so they pass the same HTTPS + private-destination guard as webhooks, both at registration and at connect time (guarded dialer).

## Delivery semantics

- Fanout enqueues one `pending` attempt per enabled subscription of each recipient profile, in the same transaction as the delivery rows. No per-reason filters: profile preferences already gate delivery creation.
- Retry schedule is short (30s/2m/10m/30m, 5 attempts): vendor push services queue messages for offline devices themselves (TTL 12h), so server-side retries only ride out transient push-service errors.
- `404`/`410` from the push service is the protocol's unsubscribe signal: the subscription row is deleted, not retried.
- `notifications.web_push_enabled` is the kill switch (default on).

## Client

- `web/public/sw.js` — displays notifications and routes clicks (episode deep link, or the inbox).
- `web/src/lib/webPush.ts` — permission + subscribe/unsubscribe flows.
- Settings → Notifications → "Browser Notifications" — this-browser toggle plus a revocable list of the profile's other subscribed devices.
