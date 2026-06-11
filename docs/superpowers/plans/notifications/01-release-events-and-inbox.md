# Profile-Scoped Release Notifications Implementation Plan

**Date:** 2026-04-09 (refined 2026-04-28, amended 2026-06-11)
**Status:** Draft (refined for multi-channel delivery; amended for back-catalog seeding, burst suppression, cross-library dedupe, dispatch outbox, and forward-sync API)
**Scope:** Foundational durable inbox + realtime websocket fanout for episode-availability notifications. This is the substrate every other channel sits on.
**Companion docs:**
- [`00-architecture-overview.md`](./00-architecture-overview.md) — read first for cross-cutting context
- [`02-apns-relay.md`](./02-apns-relay.md) — Apple push, sits on top of this foundation
- [`03-fcm-relay.md`](./03-fcm-relay.md) — Android push, sits on top of this foundation
- [`04-outbound-webhooks.md`](./04-outbound-webhooks.md) — Discord/generic webhooks, sits on top of this foundation

> **For agentic workers:** REQUIRED SUB-SKILL: Use `executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add scalable, profile-scoped notifications for newly available episodes when the series is relevant to that profile through `next up`, `continue watching`, `favorites`, or `watchlist`. Notifications must be durable, resumable, and delivered through Silo's own inbox and websocket infrastructure first; mobile push and webhooks layer on top via the channel dispatcher boundary defined in [`00-architecture-overview.md`](./00-architecture-overview.md).

**Architecture:** Treat episode availability as a series-scoped release event, not a direct user notification. When ingest makes an episode newly available inside a library, persist a single durable `release_event` for that `(library_id, series_id, episode_id)` tuple. Maintain a compact `profile_series_interest` index keyed by `(profile_id, library_id, series_id)` that records why a profile cares about a series and what episode it expects next. A background fanout worker consumes `release_events`, resolves eligible recipients through that index, inserts deduplicated `notification_deliveries`, and then hands each delivery to the channel dispatcher (websocket, APNs, FCM, webhooks). The inbox API and frontend badge read from durable deliveries, so every other channel is an accelerator rather than the source of truth.

**Tech Stack:** Go 1.26, PostgreSQL migrations, existing ingest/scanner pipeline, existing realtime events websocket, React + TanStack Query frontend

---

## Scope and Constraints

- Notify only for episodic releases in v1.
- A release means "episode became available in-library", not merely "metadata says it airs today".
- Delivery unit is the profile, not the user.
- Scaling target is driven by fanout per popular series, not total catalog size. The design must remain efficient with roughly 1 million episodes, 20,000 series, and hundreds of users (roughly 1,000 profiles) on a single server — a popular series may have several hundred interested profiles per release event.
- Do not run personalized `next up` reads at fanout time.
- Do not block scan/ingest completion on notification recipient resolution or delivery.
- Do not add background mobile push in v1.

## Success Criteria

- One newly available episode generates exactly one durable `release_event`.
- Fanout work is proportional to "profiles interested in this series in this library".
- Re-scans, retries, and metadata refreshes do not duplicate deliveries.
- Connected clients receive live updates via websocket.
- Disconnected clients see unread notifications in an inbox on reconnect.
- The system can backfill profile interest state and repair missed fanout work without manual data cleanup.

## Out of Scope

- Vendor push services, APNs, Firebase, or browser push subscriptions.
- Generic "library changed" notifications.
- Movie release notifications.
- Immediate aggregation like "3 new episodes available" in v1. Store per-episode deliveries first; aggregate presentation can come later.

---

## Data Model

### New Tables

- `episode_availability`
  - Purpose: durable record that an episode is available in a given library. Rows are inserted both by live ingest (which also emits release events) and by **seeding** (initial library scans and the feature-enable backfill, which emit no events — see "Seeding and Burst Suppression").
  - Note: `episode_id` and `series_id` are catalog-level content IDs (`media_items.content_id`); library membership is the `media_item_libraries` junction. The same episode in two libraries shares one `episode_id`.
  - Columns:
    - `library_id integer not null`
    - `episode_id text not null`
    - `series_id text not null`
    - `season_number integer not null`
    - `episode_number integer not null`
    - `episode_key integer not null`
    - `available_at timestamptz not null default now()`
    - `created_at timestamptz not null default now()`
  - Constraints:
    - primary or unique key on `(library_id, episode_id)`
  - Indexes:
    - `(library_id, series_id, episode_key desc)`

- `notification_library_seed_state`
  - Purpose: per-library marker that availability seeding has completed for the library. Release events are emitted **only** for libraries with a `seeded_at` value; until then, availability inserts are silent. This is what makes "newly available" mean *newly released to this server* instead of *newly seen by the notifications feature*.
  - Columns:
    - `library_id integer primary key references media_folders(id) on delete cascade`
    - `seeded_at timestamptz not null default now()`

- `release_events`
  - Purpose: one logical release emitted when an episode first becomes available in a library.
  - Columns:
    - `id text not null`
    - `library_id integer not null`
    - `series_id text not null`
    - `episode_id text not null`
    - `season_number integer not null`
    - `episode_number integer not null`
    - `episode_key integer not null`
    - `available_at timestamptz not null`
    - `dedupe_key text not null` — composed as `{library_id}:{episode_id}`. Exists as an explicit column (rather than relying on a composite unique) so future event kinds can share the table with their own key shapes.
    - `processed_at timestamptz`
    - `suppressed_reason text` — null for fanned-out events; `'series_burst'` when the per-series burst cap consumed this event without fanout (see "Seeding and Burst Suppression").
    - `created_at timestamptz not null default now()`
  - Constraints:
    - unique on `dedupe_key`
  - Indexes:
    - `(processed_at, created_at)`
    - `(library_id, series_id, created_at desc)`

- `profile_series_interest`
  - Purpose: compact recipient index used for fanout.
  - Columns:
    - `user_id integer not null`
    - `profile_id text not null references user_profiles(id) on delete cascade`
    - `library_id integer not null`
    - `series_id text not null`
    - `favorite boolean not null default false`
    - `watchlist boolean not null default false`
    - `continue_watching boolean not null default false`
    - `next_up_candidate boolean not null default false`
    - `last_completed_episode_key integer`
    - `next_expected_episode_key integer`
    - `last_notified_episode_key integer`
    - `updated_at timestamptz not null default now()`
  - Constraints:
    - primary key on `(profile_id, library_id, series_id)`
  - Indexes:
    - `(library_id, series_id)`
    - partial index on `(library_id, series_id)` where `favorite OR watchlist OR continue_watching OR next_up_candidate`
    - `(profile_id, updated_at desc)`

- `notification_deliveries`
  - Purpose: durable inbox rows for profiles.
  - Columns:
    - `id text not null`
    - `release_event_id text references release_events(id) on delete set null` — nullable: operational types like `webhook.auto_disabled` have no release event, and retention pruning of old `release_events` must not delete inbox rows.
    - `user_id integer not null`
    - `profile_id text not null references user_profiles(id) on delete cascade`
    - `library_id integer` — nullable; populated for `episode.available`, null for operational types.
    - `series_id text` — same nullability rule.
    - `episode_id text` — same nullability rule.
    - `type text not null`
      - v1 known values: `episode.available` (the primary case), `webhook.auto_disabled` (operational notice posted by the webhook channel when it auto-disables a profile-owned webhook — see [`04-outbound-webhooks.md`](./04-outbound-webhooks.md))
      - extensible; new types may land in future versions. Frontend must render unknown types with a generic fallback.
    - `reason_flags jsonb not null`
      - For `episode.available`: keys are the four reason booleans. For `webhook.auto_disabled`: shape is `{"webhook_id": "01J...", "webhook_name": "Family Discord", "last_failure_status": 404}` — never carries reason booleans.
    - `status text not null default 'delivered'`
    - `read_at timestamptz`
    - `delivered_at timestamptz`
    - `created_at timestamptz not null default now()`
  - Constraints:
    - `CHECK (type <> 'episode.available' OR (release_event_id IS NOT NULL AND library_id IS NOT NULL AND series_id IS NOT NULL AND episode_id IS NOT NULL))`
    - partial unique on `(profile_id, release_event_id)` where `release_event_id IS NOT NULL`
    - partial unique on `(profile_id, episode_id)` where `type = 'episode.available'` — **cross-library dedupe**: the same episode landing in two libraries (e.g., "TV" and "TV 4K") produces two release events but at most one delivery per profile; the first event processed wins and later inserts no-op via `ON CONFLICT DO NOTHING`.
  - Indexes:
    - `(profile_id, created_at desc)`
    - `(profile_id, read_at, created_at desc)`
    - `(status, created_at)`
    - `(created_at, id)` — supports the forward-sync cursor.

- `notification_preferences`
  - Purpose: per-profile notification controls.
  - Columns:
    - `profile_id text not null references user_profiles(id) on delete cascade`
    - `enabled boolean not null default true`
    - `notify_favorites boolean not null default true`
    - `notify_watchlist boolean not null default true`
    - `notify_continue_watching boolean not null default true`
    - `notify_next_up boolean not null default true`
    - `updated_at timestamptz not null default now()`
  - Constraints:
    - primary key on `profile_id`

### Shared Helpers

- Add a small shared helper for `episode_key = season_number * 1000000 + episode_number`. The 1,000,000 multiplier comfortably accommodates real-world worst cases: long-running daily soaps and absolute-numbered anime (One Piece's catalog representation can exceed 10,000 episodes in a single "season 1" when scanners flatten absolute numbering, exceeding a `* 10000` multiplier). The combined value still fits comfortably within PostgreSQL `integer` (up to 2,147,483,647) for any realistic season number. Scanners must not emit `episode_number >= 1_000_000`; document as an invariant and reject in catalog ingest.
- Use this helper everywhere that stores or compares episode progression and release state.

---

## Implementation Surface

### Backend Modules Likely to Change

- `internal/libraryingest/executor.go`
- `internal/scanner/scanner.go`
- `internal/api/handlers/favorites.go` (this file owns `PersonalDataHandler` which serves both favorites and watchlist routes — `HandleAddFavorite`, `HandleAddToWatchlist`, etc.)
- `internal/watchstate/service.go` (entry points: `SetFavorite`, `ToggleFavorite`, `RecordPlaybackStop`, `RecordImportedWatch`, `RecordImportedHistory`, `RecordJellycompatMarkPlayed`)
- `internal/api/handlers/progress.go` (the sync endpoint writes to the user store directly and must also trigger interest updaters — see Task 5)
- `internal/events/types.go` (extend `AllChannels` with `ChannelNotifications`)
- `internal/api/handlers/events_ws.go` (handshake must validate active profile via a short-lived ticket; see Task 8)
- `internal/api/router.go`
- `web/src/components/RealtimeEventsProvider.tsx`
- new package under `internal/notifications` (preferred — there is already a stub package at `internal/notifications/hub.go` for the catalog/jobs realtime hub; the user-notification system is a sibling concern in the same package directory, but the existing `Hub` type is operational-events-only and should not be conflated)
- `internal/taskmanager/tasks/...`
- new migrations under `migrations/`

### New Backend Ownership Boundaries

- `internal/notifications` (existing package — extends with new files)
  - availability repo
  - release event repo
  - profile interest repo
  - fanout worker
  - notification delivery repo
  - eligibility logic
  - channel dispatcher interface and implementations (see "Channel Dispatcher Boundary" below)
  - websocket dispatcher implementation
  - per-channel dispatcher implementations land in subsequent specs (`02-apns-relay.md`, `03-fcm-relay.md`, `04-outbound-webhooks.md`)

- `internal/api/handlers/notifications.go`
  - inbox, unread count, preferences APIs
  - capability endpoint for clients
  - webhook CRUD (delegated handler under `internal/api/handlers/notifications_webhooks.go` per `04-outbound-webhooks.md`)

> **Note on the existing `internal/notifications/hub.go`:** The current `notifications.Hub` type is a thin wrapper around `internal/events/Hub` for publishing operational events on `ChannelCatalog` and `ChannelJobs` (library-changed, metadata-updated, job lifecycle). It is unrelated to the user-facing notification system designed here. Do not conflate the two. The user-notification fanout publishes on the new `ChannelNotifications` and uses its own publishers; the existing operational hub continues to own catalog/jobs realtime.

### Channel Dispatcher Boundary

The fanout worker creates `notification_deliveries` rows. After each row commits, it hands the delivery to a `Dispatcher`, which fans the delivery out to channels:

```go
type Dispatcher interface {
    // Called once per notification_deliveries row, AFTER the row commits.
    // Per-target dispatchers (push, webhooks) do not decide their own work:
    // the fanout transaction already enqueued one `pending` attempt row per
    // (delivery_id, target_id). Dispatch claims and sends those rows; a
    // recovery worker sweeps `pending` rows whose claim never happened.
    // Idempotency is by (delivery_id, target_id, attempt_number). The
    // websocket dispatcher has no per-target row and is idempotent by
    // delivery_id alone.
    Dispatch(ctx context.Context, delivery NotificationDelivery) error
}
```

V1 implementations:

- `WebsocketDispatcher` — publishes a `notification.created` event on `ChannelNotifications` scoped to `(user_id, profile_id)`. Best-effort; the durable row is the source of truth.
- `ApplePushDispatcher` — see [`02-apns-relay.md`](./02-apns-relay.md).
- `AndroidPushDispatcher` — see [`03-fcm-relay.md`](./03-fcm-relay.md).
- `WebhookDispatcher` — see [`04-outbound-webhooks.md`](./04-outbound-webhooks.md).

A composite `MultiDispatcher` runs all configured dispatchers in parallel with bounded concurrency. Channel failures are isolated: a downed APNs path does not block the websocket or webhook channels.

---

## Canonical Semantics

These are the exact behavioral rules the implementation should follow.

### What Counts as a Release

- A release is emitted only when an episode becomes newly available inside a specific library **that has completed availability seeding** (`notification_library_seed_state.seeded_at` is set — see "Seeding and Burst Suppression" below).
- "Available" means:
  - the episode is resolved to a concrete episode record
  - the episode has at least one playable non-missing file in that library
  - the `(library_id, episode_id)` pair did not previously exist in `episode_availability`
- Metadata-only changes do not create a release.
- Repaired probes, poster refreshes, and title edits do not create a release.
- Removing an episode's file and later re-adding it (quality upgrades, re-downloads) does not re-notify: the `episode_availability` row persists across file churn. This is intentional — availability is a one-way "first released here" fact.
- If the same episode is available in two libraries, that is two distinct availability facts and can yield two distinct `release_events`. Delivery-level dedupe (below) ensures a profile is still notified at most once per episode.

### Seeding and Burst Suppression

The flood problem: without these rules, the first scan of a new library — or adding a 200-episode back-catalog of a favorited series — makes every episode "newly available" at once. On a server with hundreds of users, one bulk import could generate tens of thousands of deliveries and pushes in a single scan. "Newly available" must mean *newly released to this server*, not *newly seen by the notifications feature*.

**Seeding rules (no release events):**

- **Feature-enable seeding.** Before `notifications.release_events_enabled` may be turned on, a one-time seeding task inserts `episode_availability` rows for every currently playable episode in every library, without creating release events, and writes `notification_library_seed_state` for each library. This is a rollout prerequisite, like the interest backfill.
- **New-library seeding.** A library with no `notification_library_seed_state` row is unseeded: availability inserts during its scans create no release events. When the library's first full scan completes successfully, the scanner writes the seed marker. Subsequent scans emit release events normally.
- Seeding is idempotent (`ON CONFLICT DO NOTHING` on `episode_availability`; upsert on the seed marker).

**Burst suppression (bounded fanout for bulk additions to seeded libraries):**

Bulk additions to an *existing* library (a back-catalog season pack, a batch import) legitimately create many release events. The fanout worker bounds the per-profile blast radius:

- **Settling delay.** The worker claims only release events with `created_at <= now() - settle_seconds` (default `30`, setting `notifications.fanout.settle_seconds`) so one scan's burst for a series lands in the same claim batch instead of trickling through several.
- **Per-series burst cap.** Within a claim batch, events are grouped by `(library_id, series_id)`. If a group exceeds `notifications.fanout.max_series_burst` (default `3`), the worker fans out only the `max_series_burst` events with the highest `episode_key` and marks the rest `processed_at = now(), suppressed_reason = 'series_burst'` — no deliveries, no pushes, no webhooks for the suppressed events.
- The cap is per claim batch and therefore approximate across batches; that is acceptable. A v2 aggregated delivery type ("12 episodes of X are now available") can replace suppression with a summary row without schema changes.
- Suppressed events do not update `last_notified_episode_key`; the fanned-out events do, and the guarded max-wins update makes ordering irrelevant.
- Suppression counts are logged and exported (`release_events_suppressed_total`) so admins can see what was withheld; silent truncation is not acceptable.

### What Counts as an Interested Profile

A profile is a candidate recipient only if all of the following are true:

- the profile belongs to the authenticated `user_id`
- the profile can access the `library_id` where the episode became available
- the profile has a `profile_series_interest` row for the same `(library_id, series_id)`
- at least one of the interest flags is enabled and relevant

For v1, "can access the library" should be determined from profile restrictions and the same library visibility rules already used in request-time access filtering. The index must never assume global library visibility.

### When Each Interest Flag Should Be Set

- `favorite`
  - set when a series is explicitly favorited
  - cleared when the favorite is removed
- `watchlist`
  - set when a series is explicitly watchlisted
  - cleared when the watchlist entry is removed
- `continue_watching`
  - set when the profile has in-progress episode progress for the series
  - cleared when no qualifying in-progress state remains
- `next_up_candidate`
  - set when the profile has progression state that should receive future-episode notifications
  - paired with `next_expected_episode_key`
  - cleared when the series is no longer eligible for next-up style notifications

### Notification Deduplication Rules

- One `release_event` per logical `(library_id, episode_id)` availability.
- One `notification_delivery` per `(profile_id, release_event_id)`.
- **At most one `episode.available` delivery per `(profile_id, episode_id)` across libraries.** Media items are catalog-level, so the same episode in "TV" and "TV 4K" shares one `episode_id`; the partial unique index makes the first-processed release event win and later ones no-op for that profile. Dual-quality library setups must not double-notify.
- If multiple interest reasons apply, store one delivery with merged `reason_flags`.
- Reprocessing a release event must be safe and produce zero duplicates.

### Profile Scope Rules

- Inbox APIs are scoped to the active `X-Profile-Id`.
- Realtime notifications must carry both `user_id` and `profile_id`.
- The websocket layer must reject events whose `profile_id` does not match the request context profile.
- Admin users do not get a cross-profile notifications view in v1 unless explicitly added later.
- Because browser websocket connections cannot set arbitrary request headers in the normal `WebSocket` API, the websocket handshake must carry profile identity explicitly — via a **short-lived single-use ticket** minted over authenticated REST (see "Websocket Handshake Contract" below). Long-lived profile tokens must not appear in the handshake query string: self-hosted servers commonly sit behind reverse proxies whose access logs capture query strings.

---

## End-to-End Lifecycle

1. Ingest makes an episode newly available in a library.
2. The backend inserts a new `episode_availability` row.
3. That insert produces one durable `release_event` (only for seeded libraries — see "Seeding and Burst Suppression").
4. A fanout worker claims unprocessed `release_events` past the settling delay and applies the per-series burst cap.
5. The worker loads candidate recipients from `profile_series_interest`.
6. The worker filters recipients using preferences and progression rules.
7. The worker bulk inserts `notification_deliveries` **and the `pending` per-target outbox attempt rows for the push and webhook channels** in the same transaction.
8. The worker marks the `release_event` processed.
9. The worker publishes realtime events for inserted deliveries and triggers the per-channel dispatchers, which claim their pending attempt rows.
10. Connected clients update immediately; disconnected clients see the inbox snapshot later. If the process crashes between steps 8 and 9, recovery workers drain the pending attempt rows.

No step after `release_event` creation is allowed to run inline on the ingest request path.

---

## Transaction and Concurrency Rules

### Availability and Event Creation

- Availability detection and `release_event` creation should happen in a short transaction where practical.
- Use `INSERT ... ON CONFLICT DO NOTHING RETURNING ...` patterns to detect new rows without race-prone pre-checks.
- Prefer deriving "newly available episodes" from touched content IDs or touched libraries inside the current ingest scope.
- Do not rescan the entire library to decide what changed.

### Fanout Worker Claiming

- Process `release_events` in batches.
- Claim events with a concurrency-safe pattern such as:
  - `SELECT ... FOR UPDATE SKIP LOCKED`
  - then mark a claim timestamp or process inside the transaction
- Multiple Silo nodes must be able to run the worker without duplicate fanout.
- If the process crashes after inserting deliveries but before marking the event processed, reprocessing must be harmless because delivery inserts are idempotent.

### Delivery Insert and State Update

- The worker should:
  1. load candidate interest rows
  2. compute eligible recipients
  3. bulk insert deliveries with `INSERT ... ON CONFLICT DO NOTHING RETURNING id, profile_id` so the worker can distinguish newly inserted rows (publish realtime + per-channel dispatch) from deduped rows (no-op)
  4. **enqueue the dispatch outbox**: for each *newly inserted* delivery, insert `pending` attempt rows — one per enabled `push_device` of the recipient profile (`push_delivery_attempts`, see `02`/`03`) and one per enabled, reason-matching `notification_webhook` (`webhook_delivery_attempts`, see `04`). Skip channels whose feature flag is off or whose tables have not landed yet; the foundation defines the pattern, the channel specs own the tables.
  5. update `last_notified_episode_key` using a guarded `UPDATE profile_series_interest SET last_notified_episode_key = $newKey WHERE profile_id = $p AND library_id = $l AND series_id = $s AND (last_notified_episode_key IS NULL OR last_notified_episode_key < $newKey)`. The `< $newKey` guard prevents two concurrent workers handling adjacent release events from clobbering each other's update — the higher key always wins regardless of commit order.
  6. mark the `release_event` processed
- Steps 3-6 should happen in one transaction so the event does not get marked processed without durable deliveries **and** durable dispatch intent. This is the outbox invariant from `00`: a crash after commit delays pushes/webhooks (recovery workers sweep stale `pending` rows) instead of silently dropping them. Without step 4, "durable rows + retry tables guarantee eventual delivery" would be false — retry workers can only retry attempts that were recorded.
- Outbox sizing: pending rows are bounded by recipients × enabled targets per event, and the per-series burst cap bounds events per scan. Chunk the inserts like the delivery inserts.
- The `RETURNING` clause in step 3 is load-bearing for two invariants: "publish only inserted rows" and "no duplicate websocket event on rescan." The dispatcher and the outbox enqueue must operate on the returned set, not the candidate set.

### Realtime Publishing

- Publish websocket events only after the delivery transaction commits.
- If realtime publish fails, the durable delivery still exists and will appear on reconnect.

---

## Exact Recipient Resolution Rules

### Library Visibility

- `profile_series_interest` is library-scoped on purpose.
- When building or updating interest rows, only create rows for libraries the profile can actually see.
- If profile library restrictions change, affected `profile_series_interest` rows must be rebuilt.

### Series Resolution

- Favorites/watchlist against series items map directly to `series_id = media_item_id`.
- Favorites/watchlist against episode or season items should resolve to their parent `series_id`.
- Movie targets are ignored in v1 and should not create `profile_series_interest`.

### Progression Cursor Rules

- `last_completed_episode_key` tracks the highest sequentially completed episode for the series as represented by the profile's watch state.
- `next_expected_episode_key` should be the next unwatched episode key after the profile's completed progression, not merely "highest seen + 1" if gaps exist.
- If implementation-time computation of true gaps is expensive, use a safe conservative value that may under-notify rather than over-notify. Document that tradeoff in code comments if used.
- `last_notified_episode_key` prevents repeated notifications for the same or older episodes.

### Home Dismissals

- Existing home-surface dismissal state should not suppress release notifications in v1.
- Dismissing `continue watching` or `next up` rows on the home screen does not mean "stop release notifications for this series".
- Notification preferences, not home-surface dismissals, are the suppression mechanism.

---

## API Contract

These shapes should be treated as part of the plan rather than decided later.

### `GET /api/v1/notifications`

Query params:

- `status=all|unread` default `all`
- `limit` default `25`, max `100`
- `before` optional RFC3339 timestamp or opaque cursor, choose the format that best matches existing API style and keep it stable

Response shape:

```json
{
  "notifications": [
    {
      "id": "01H...",
      "type": "episode.available",
      "profile_id": "profile-1",
      "library_id": 7,
      "series_id": "series-123",
      "episode_id": "episode-456",
      "series_title": "Severance",
      "episode_title": "Hello, Ms. Cobel",
      "season_number": 2,
      "episode_number": 1,
      "poster_path": "metadata/posters/...",
      "poster_thumbhash": "....",
      "reason_flags": {
        "favorite": true,
        "watchlist": false,
        "continue_watching": true,
        "next_up": true
      },
      "created_at": "2026-04-09T12:34:56Z",
      "read_at": null
    }
  ]
}
```

### `GET /api/v1/notifications/sync`

Forward sync for clients that wake from a push or reconnect after an offline gap. This is the endpoint mobile clients call after an APNs/FCM wake (see `02`/`03` "Wake And Metadata Fetch") — push delivery is not guaranteed and multiple deliveries may have accumulated, so a cursor sync beats fetching one delivery by ID.

Query params:

- `since` optional opaque cursor from a previous response. Encodes `(created_at, id)` and pages **ascending** (oldest first), the opposite direction of the inbox list. Omitted: returns the most recent page and a cursor for subsequent calls.
- `limit` default `50`, max `100`

Response shape:

```json
{
  "notifications": [ /* same row shape as GET /api/v1/notifications */ ],
  "next_cursor": "opaque",
  "unread_count": 3
}
```

- Rows include read state so a wake-sync can render accurately without a second call; `unread_count` is included for the same reason.
- Clients persist `next_cursor` per `(server, profile)` and pass it on the next wake.

### `GET /api/v1/notifications/{id}`

Returns a single delivery by ID, same row shape as the list API. Profile-scoped: returns `404` if the delivery belongs to another profile. Used when a push wake carries a specific `delivery_id` and the app deep-links to one notification.

### `GET /api/v1/notifications/unread-count`

Response shape:

```json
{
  "count": 3
}
```

Unread semantics are `read_at IS NULL` for the active profile. The `status` field is operational metadata and should not change unread-count behavior.

### `POST /api/v1/notifications/{id}/read`

- Marks a single notification as read for the active profile.
- Must be idempotent.
- Returns `204 No Content`.

### `POST /api/v1/notifications/read-all`

- Marks all notifications as read for the active profile.
- Returns `204 No Content`.

### Realtime Event Payload

Channel: `notifications`

Event name:

- `notification.created`
- optionally later `notification.read`

Payload shape:

```json
{
  "id": "01H...",
  "profile_id": "profile-1",
  "type": "episode.available",
  "library_id": 7,
  "series_id": "series-123",
  "episode_id": "episode-456",
  "series_title": "Severance",
  "episode_title": "Hello, Ms. Cobel",
  "season_number": 2,
  "episode_number": 1,
  "reason_flags": {
    "favorite": true,
    "watchlist": false,
    "continue_watching": true,
    "next_up": true
  },
  "created_at": "2026-04-09T12:34:56Z",
  "read_at": null
}
```

The notifications snapshot should return the same object shape as the list API for recent unread rows.

### Websocket Handshake Contract

Profile identity is carried by a short-lived single-use ticket, not by tokens in the query string (reverse proxies log query strings; a leaked ticket that expired 30 seconds after minting is harmless, a leaked profile token is not).

- `POST /api/v1/events/ws-ticket` — normal auth + `X-Profile-Id`. Returns `{ "ticket": "opaque", "expires_in": 30 }`. The ticket is single-use, bound server-side to `(user_id, profile_id)`, and stored in memory (or Redis when multiple nodes serve websockets).
- The frontend websocket URL builder requests a ticket immediately before connecting and passes it as a `ticket` query parameter on `/events/ws`.
- The handshake consumes the ticket and binds the connection to the `(user_id, profile_id)` the ticket was minted for.
- If the ticket is missing, expired, already used, or invalid:
  - reject the notifications channel subscription
  - or fail the websocket handshake for profile-scoped usage
- On reconnect, the client mints a fresh ticket; tickets are cheap.
- Do not rely on `RequireProfile` middleware for `/events/ws` in the browser path.

---

## Frontend Behavior Contract

- The unread badge should reflect only the active profile.
- Switching profiles should clear notifications query cache and resubscribe under the new `X-Profile-Id`.
- On websocket connect:
  - subscribe to `notifications`
  - hydrate unread rows from the snapshot
  - update unread count cache
- On `notification.created`:
  - prepend to cached inbox list if present
  - increment unread count if `read_at == null`
  - show a toast only when the active profile matches
- On reading a notification:
  - update cached row state
  - decrement unread count without waiting for a refetch

Do not piggyback on `catalog` invalidation for inbox behavior. Notifications need their own query keys and event handling.

---

## Execution Plan

### Task 1: Add Release and Notification Persistence Schema

**Files:**
- Add: a timestamped Goose migration in `migrations/sql/` created with `make migrate-create NAME=profile_release_notifications` (single file with `-- +goose Up` / `-- +goose Down` sections; do **not** create paired `.up.sql` / `.down.sql` files — that convention predates this repo's migration runner).

- [ ] Create `episode_availability`, `notification_library_seed_state`, `release_events`, `profile_series_interest`, `notification_deliveries`, and `notification_preferences`.
- [ ] Add unique constraints, partial unique indexes (cross-library dedupe), foreign keys, and indexes exactly as described above.
- [ ] Keep `notification_deliveries.reason_flags` as `jsonb` for merged-source recording without a schema churn loop.
- [ ] Ensure the `-- +goose Down` section cleanly removes the new tables and indexes in reverse order.

### Task 2: Introduce a Release Package and Shared Episode Key Helper

**Files:**
- Add: `internal/notifications/types.go`
- Add: `internal/notifications/episode_key.go`
- Add: `internal/notifications/repositories.go`

- [ ] Define domain types for availability records, release events, profile interest, preferences, and deliveries.
- [ ] Add `EpisodeKey(seasonNumber, episodeNumber int) int`.
- [ ] Add repository methods for:
  - recording episode availability
  - inserting release events idempotently
  - selecting unprocessed release events
  - loading profile interest rows by `(library_id, series_id)`
  - bulk inserting deliveries
  - marking release events processed
  - marking notifications read
  - loading inbox pages and unread counts
  - loading and upserting notification preferences

### Task 3: Detect Newly Available Episodes During Ingest

**Files:**
- Modify: `internal/scanner/scanner.go`
- Modify: `internal/libraryingest/executor.go`
- Add or modify: `internal/notifications/availability_detector.go`

- [ ] Add a path that identifies which episode IDs became newly available in the touched ingest scope.
- [ ] Persist availability through `episode_availability` with `ON CONFLICT DO NOTHING`.
- [ ] Only create `release_events` for newly inserted availability rows **in libraries that have a `notification_library_seed_state` row**; unseeded libraries insert availability silently.
- [ ] Write the seed marker when a new library's first full scan completes successfully.
- [ ] Run this after matching/reconcile is complete, not on raw file discovery, so the release is tied to an actual resolved episode.
- [ ] Keep this write path lightweight and transactional where practical.
- [ ] Do not perform recipient lookup or websocket publishing here.
- [ ] Ensure the implementation works for:
  - full-library ingest
  - subtree ingest
  - single-file ingest
- [ ] For file/subtree ingest, use touched content IDs rather than broad library reconciliation queries.
- [ ] Resolve the current ingest seam limitation explicitly: the existing `Matcher` interface only returns counts, not content IDs or episode IDs. Choose one implementation path before coding:
  - widen the matcher contract to return touched content IDs or release candidates
  - or add a post-match repository query that derives newly available episode IDs from the just-touched ingest scope without scanning the full library
- [ ] Prefer the post-match repository query if it keeps the matcher contract stable and remains scope-bounded.

**Implementation note:** The current ingest publish seam in [`internal/libraryingest/executor.go`](../../../../internal/libraryingest/executor.go) is the right place to persist release candidates because it already represents a completed scan/match cycle.

### Task 4: Maintain `profile_series_interest` from Favorites and Watchlist

**Files:**
- Modify: `internal/api/handlers/favorites.go`
- Add: `internal/notifications/interest_updater.go`

- [ ] When a profile favorites or watchlists a series, upsert `profile_series_interest` rows for every visible library membership of that series.
- [ ] When a profile removes a favorite or watchlist item, clear only that flag; do not delete the row if other interest flags remain.
- [ ] Ignore movie items in v1.
- [ ] Resolve "target item to series" for episode or season cases so the interest key stays series-centric.
- [ ] Keep the write best-effort but logged if it fails, similar to other auxiliary user-state updates.
- [ ] Respect profile library restrictions when creating library-scoped interest rows.
- [ ] Add a helper that recomputes a single `(profile, series)` interest row from source-of-truth state so repairs and live updates share code.

### Task 5: Maintain `profile_series_interest` from Playback and History

**Files:**
- Modify: `internal/watchstate/service.go`
- Possibly modify: `internal/api/handlers/playback.go`
- Modify: `internal/api/handlers/progress.go`
- Add or modify: `internal/notifications/interest_progress.go`

- [ ] Trigger the updater on watch-state **transitions**, not every progress write: an episode entering in-progress state, crossing the completion threshold, or progress rows being deleted. Progress sync endpoints fire continuously during playback on a busy server (hundreds of concurrent streams); recomputing interest on every tick is a pointless hot write path. Compare the derived flags/cursors against the existing row (or debounce per `(profile, series)` with a short TTL) and skip no-op writes.
- [ ] On episode watch progress transitions, update `continue_watching` and `next_up_candidate`.
- [ ] On episode completion, update:
  - `last_completed_episode_key`
  - `next_expected_episode_key`
  - `next_up_candidate`
- [ ] Clear `continue_watching` when the profile has no in-progress state left for the series.
- [ ] Keep these updates series-centric and library-aware.
- [ ] Do not query the full catalog at fanout time to derive these values later.
- [ ] Decide one implementation path and encode it in code:
  - either fully recompute a profile-series row from source-of-truth progress on every mutation
  - or apply an incremental updater with a shared repair path
- [ ] Prefer recompute-per-series on mutation if the series-local query cost is acceptable; it is simpler and less drift-prone than trying to patch every field incrementally.
- [ ] Cover every live progress mutation path, not just `watchstate.Service`. The current sync endpoint in `internal/api/handlers/progress.go` writes directly to the user store, so it must also trigger the shared profile-series updater or be refactored behind the same abstraction.

**Decision:** `next_up_candidate` should mean "this profile is eligible for next-episode notifications on this series", while `next_expected_episode_key` is the precise cursor used to decide if a newly available episode should notify.

### Task 6: Add Backfill / Repair Tasks for Interest State and Availability Seeding

**Files:**
- Add: `internal/taskmanager/tasks/rebuild_release_interest.go`
- Add: `internal/taskmanager/tasks/seed_episode_availability.go`
- Modify: `internal/api/router.go`

- [ ] Add the availability seeding task: insert `episode_availability` for every currently playable episode in every library (batched, `ON CONFLICT DO NOTHING`), write `notification_library_seed_state` per library, emit zero release events. Must complete before `notifications.release_events_enabled` is turned on.
- [ ] Add a hidden task that incrementally rebuilds `profile_series_interest` from existing favorites, watchlist, and watch progress data.
- [ ] Process in batches to avoid long-running transactions.
- [ ] Make the task rerunnable and idempotent.
- [ ] Wire it into the task manager similarly to existing scheduled tasks.
- [ ] Keep the initial rollout safe by allowing the task to run before fanout is enabled.
- [ ] Add an admin-invokable entry point if there is already a pattern for manually triggering tasks in this repo.

### Task 7: Implement the Fanout Worker

**Files:**
- Add: `internal/notifications/fanout_worker.go`
- Add: `internal/notifications/fanout_logic.go`
- Modify: `internal/api/router.go`
- Possibly modify: `cmd/silo/main.go`

- [ ] Add a worker loop that loads unprocessed `release_events` in batches, honoring the settling delay (`created_at <= now() - settle_seconds`).
- [ ] Nudge the worker on insert instead of relying on tight polling: ingest publishes a wake signal (Postgres `LISTEN/NOTIFY` or a Redis pub — Redis is already a dependency) after writing release events; the worker also polls at a relaxed fallback interval (15-30s). The nudge schedules a claim at `settle_seconds` so notifications still feel near-realtime.
- [ ] Group claimed events by `(library_id, series_id)` and apply the per-series burst cap: fan out only the `max_series_burst` highest `episode_key` events per group; mark the rest processed with `suppressed_reason = 'series_burst'`.
- [ ] For each fanned-out event, load `profile_series_interest` rows by `(library_id, series_id)`.
- [ ] Apply eligibility rules:
  - notify if `favorite`
  - notify if `watchlist`
  - notify if `continue_watching`
  - notify if `next_up_candidate` and `episode_key >= next_expected_episode_key`
  - suppress if `last_notified_episode_key >= episode_key`
  - suppress if preferences disable all matching reasons
- [ ] Bulk insert `notification_deliveries` with `ON CONFLICT DO NOTHING` (both partial uniques participate: per-release-event and cross-library per-episode).
- [ ] Enqueue the dispatch outbox in the same transaction: `pending` attempt rows per enabled push device / reason-matching webhook for each newly inserted delivery (skip channels whose flags are off or whose tables haven't landed).
- [ ] Update `last_notified_episode_key` for rows that actually produced deliveries.
- [ ] Mark the `release_event` processed only after durable delivery and outbox rows are created.
- [ ] Keep worker throughput visible through structured logs and counters, including suppression counts.
- [ ] Use `FOR UPDATE SKIP LOCKED` or an equivalent claim pattern so multiple nodes can process safely.
- [ ] Publish realtime events only for deliveries inserted in the current transaction, not for deduped rows.
- [ ] Batch processing defaults:
  - claim up to `100` release events at a time
  - load recipients in memory per event
  - bulk insert deliveries and outbox rows in chunks if recipient counts are large

### Task 8: Add a Notifications Realtime Channel and Snapshot

**Files:**
- Modify: `internal/events/types.go`
- Modify: `internal/api/handlers/events_ws.go`
- Add or modify: `internal/events/publishers.go`
- Modify: `web/src/components/RealtimeEventsProvider.tsx`
- Modify: `web/src/api/client.ts` or a nearby profile-token source if needed

- [ ] Add `ChannelNotifications` to `internal/events/types.go`. Must be appended to the `AllChannels` slice (currently: catalog, jobs, sessions, tasks, scans, history_import, user_state, plugins) so subscription enumeration finds it.
- [ ] Update `allowedChannelsForRole` in `internal/api/handlers/events_ws.go` to permit authenticated users (and admins) to subscribe to `ChannelNotifications`.
- [ ] Update the snapshot switch in `events_ws.go` to return a real snapshot payload for `ChannelNotifications` (recent unread deliveries for the bound profile, capped at e.g. 25 rows, identical row shape to the inbox list API in Task 9). Not `null`.
- [ ] Publish a profile-scoped realtime event when new `notification_deliveries` are created. Include both `user_id` and `profile_id` on the event payload.
- [ ] Ensure the websocket filter continues to respect `UserID`, and add profile scoping for notifications.
- [ ] Add the `POST /api/v1/events/ws-ticket` endpoint (normal auth + `X-Profile-Id`) returning a ~30-second single-use ticket bound to `(user_id, profile_id)`, and extend the websocket handshake to consume the `ticket` query parameter and bind the connection. `/events/ws` is not currently using `RequireProfile` and the browser websocket path does not send `X-Profile-Id`; long-lived profile tokens must not be passed in the query string (see "Websocket Handshake Contract").
- [ ] Store tickets in memory for single-node deployments; use Redis when multiple nodes serve websockets.
- [ ] Add explicit tests for profile mismatch rejection and expired/reused-ticket rejection.

**Important safety note:** The existing websocket path filters on `UserID` but not generally on `ProfileID`. Notification events should include both, and the handler should reject deliveries for mismatched profiles.

### Task 8a: Wire Worker Startup and Shutdown

**Files:**
- Modify: `cmd/silo/main.go`
- Possibly modify: `internal/api/router.go`

- [ ] Construct release repositories and services when DB access is available.
- [ ] Start the fanout worker under the main application context.
- [ ] Ensure graceful shutdown waits for in-flight worker loops to exit cleanly.
- [ ] Do not start the worker when the feature flag is disabled.
- [ ] Keep the long-running fanout worker separate from taskmanager scheduled tasks. Use taskmanager only for rebuild, repair, and cleanup passes.

### Task 9: Add Inbox and Read APIs

**Files:**
- Add: `internal/api/handlers/notifications.go`
- Modify: `internal/api/router.go`

- [ ] Add:
  - `GET /api/v1/notifications`
  - `GET /api/v1/notifications/sync` (forward cursor; the mobile wake-fetch endpoint required by `02`/`03`)
  - `GET /api/v1/notifications/{id}`
  - `GET /api/v1/notifications/unread-count`
  - `POST /api/v1/notifications/{id}/read`
  - `POST /api/v1/notifications/read-all`
- [ ] Mount these routes behind the same auth + profile middleware pattern used by other profile-scoped endpoints so `X-Profile-Id` is mandatory.
- [ ] Return enough display data for the frontend to render a useful row without an extra lookup round trip:
  - series title
  - episode title if available
  - poster/thumbhash
  - season/episode numbers
  - created timestamp
  - reason flags
- [ ] Keep pagination cursor-based or limit/offset-based, whichever matches local patterns best.
- [ ] Add endpoints for preferences if they are needed to make the feature operable in v1:
  - `GET /api/v1/notifications/preferences`
  - `PUT /api/v1/notifications/preferences`

### Task 9a: Add Notification Preferences API

**Files:**
- Add or modify: `internal/api/handlers/notifications.go`
- Modify: `internal/api/router.go`

- [ ] Expose profile-scoped preferences for enable/disable and per-reason toggles.
- [ ] Default missing rows to all-enabled behavior.
- [ ] Keep preference writes idempotent.

### Task 10: Add Frontend Inbox, Badge, and Live Updates

**Files:**
- Modify: `web/src/components/RealtimeEventsProvider.tsx`
- Add: `web/src/hooks/queries/notifications.ts`
- Add: `web/src/pages/Notifications.tsx`
- Modify app shell/sidebar files as needed

- [ ] Add query keys and hooks for inbox pages and unread counts.
- [ ] Subscribe the frontend to the `notifications` channel.
- [ ] Hydrate snapshot state on connect and update unread counts on live events.
- [ ] Add a sidebar badge or header badge for unread count.
- [ ] Add a dedicated notifications page.
- [ ] Add a lightweight live toast for connected clients.
- [ ] Mark notifications read when opened or through explicit actions.
- [ ] Add profile-scoped preferences UI if the API is included in v1.
- [ ] Clear notification caches when auth state or active profile changes.

### Task 11: Add Operational Repair Paths and Feature Flags

**Files:**
- Add or modify: `internal/notifications/repair.go`
- Modify: config or settings files if runtime flags are preferred

- [ ] Add a repair path that finds unprocessed `release_events` or events with missing deliveries.
- [ ] Add a rebuild path for corrupted or stale `profile_series_interest`.
- [ ] Gate rollout with feature flags or settings:
  - `notifications.release_events_enabled`
  - `notifications.fanout_enabled`
  - `notifications.ui_enabled`
  - `notifications.preferences_enabled` if preference UI/API is shipped separately
- [ ] Enable in stages: schema -> backfill -> event creation -> fanout -> UI.
- [ ] Add a retention policy decision and implement it:
  - keep read notifications for a bounded window such as 90 days
  - keep processed `release_events` for a bounded debugging window
  - add cleanup tasks if retention is not indefinite

---

## Eligibility Rules

These are implementation decisions, not open questions.

- `favorite`: notify whenever a newly available episode lands for a favorited series.
- `watchlist`: notify whenever a newly available episode lands for a watchlisted series.
- `continue_watching`: notify whenever a newly available episode lands for a series the profile is actively watching.
- `next_up`: notify only when the new episode is at or beyond the profile's `next_expected_episode_key`.
- Multiple reasons produce one delivery with merged `reason_flags`.
- Re-scans and repeated availability writes must not produce duplicate deliveries.

---

## Scaling Notes

- The hot fanout query must be `profile_series_interest WHERE library_id = ? AND series_id = ?`.
- Do not use the full `episodes` table in delivery-time recipient resolution.
- Do not execute [`internal/catalog/nextup_repo.go`](../../../../internal/catalog/nextup_repo.go) per recipient.
- The design should scale linearly with "interested profiles for this one series", which is acceptable even for popular shows.
- With roughly 20,000 series, series-scoped state remains compact and cache-friendly compared to episode-scoped per-profile tracking.
- Size expectations:
  - `episode_availability` can grow with available episodic catalog and should be indexed narrowly
  - `profile_series_interest` grows with engaged profile-series pairs, not total episodes
  - `notification_deliveries` is the fastest-growing table and needs retention and pagination discipline
- Fanout throughput should be measured in:
  - release events per minute
  - recipients per event
  - deliveries inserted per second
- The worker should remain correct if a hit show has very large recipient counts. Use batching rather than one enormous insert statement when needed.
- Hundreds-of-users arithmetic (sanity check, ~1,000 profiles): a popular release with 300 interested profiles inserts 300 delivery rows plus outbox rows — trivial for PostgreSQL. The dangerous shapes are bulk imports (bounded by seeding + the per-series burst cap) and relay throughput (bounded by client-side pacing in `02`/`03`). Steady state of ~100 new episodes/day × ~20 interested profiles each is ~2,000 deliveries/day; retention keeps `notification_deliveries` in the low millions worst case.

---

## Rollout Plan

1. Deploy schema only.
2. Deploy `profile_series_interest` updaters and the rebuild task with fanout disabled.
3. Run the interest backfill and verify row counts.
4. Run the availability seeding task and verify every library has a `notification_library_seed_state` row.
5. Enable `release_events` creation only.
6. Verify that new episodes create one durable event each and that back-catalog rescans create none.
7. Enable fanout worker.
8. Verify durable deliveries without frontend changes.
9. Enable websocket channel and inbox UI.
10. Enable preferences API/UI if included.
11. Monitor metrics and repair task output before widening rollout.

---

## Backfill Strategy

The backfill is part of the implementation, not an optional follow-up.

### Initial Backfill Sources

- `user_favorites`
- `user_watchlist`
- `user_watch_progress`
- enough series metadata to resolve episode progress rows back to `series_id`
- profile library restriction settings

### Backfill Algorithm

- Iterate profiles in batches, ordered by `profile_id` (any stable ordering works; profile_id is convenient).
- Persist a checkpoint row keyed by task name (e.g., in an existing `task_state` / `kv_state` table, or a small dedicated `notification_backfill_state` table with columns `task text primary key`, `last_processed_profile_id text`, `started_at`, `updated_at`, `completed_at`). Decide between the two during Task 6 implementation; the simplest option is a single dedicated table since this is the only checkpoint the notification system needs.
- For each profile (in `WHERE profile_id > $checkpoint ORDER BY profile_id LIMIT $batch_size` order):
  - load visible libraries
  - load favorited/watchlisted series
  - load episodic progress rows and resolve to series
  - recompute one `profile_series_interest` row per `(library_id, series_id)`
- Upsert the rebuilt rows.
- After each batch, update the checkpoint row with the highest `profile_id` processed; commit. A crash between batches resumes from the checkpoint with at most one batch of repeated work (cheap because upserts are idempotent).
- Mark the checkpoint `completed_at` when the iteration sees zero remaining profiles. Subsequent reruns are no-ops unless `completed_at` is reset.
- Optionally prune rows that no longer have any interest flags and no progression cursor state.

### Backfill Safety Rules

- Backfill must be idempotent (recomputing a profile's interest rows produces the same upsert outcome regardless of how many times it runs).
- Backfill must be resumable via the checkpoint described above. A crash mid-batch leaves work to redo for at most one batch; a crash between batches resumes exactly where the checkpoint left off.
- Backfill must not emit notifications.
- Fanout must remain disabled until the first backfill has completed successfully or reached an acceptable coverage threshold.

---

## Verification Plan

Per repo guidance, keep verification proportional and backend-focused.

- [ ] Unit test episode key helpers and fanout eligibility logic.
- [ ] Repository tests for:
  - idempotent `episode_availability` insert
  - idempotent `release_events` insert
  - bulk delivery dedupe
  - unread count and read APIs
- [ ] Worker tests for:
  - one release -> many deliveries
  - repeat processing -> no duplicates
  - `next_up` cursor gating
  - merged reason flags
  - multi-node claim safety
  - profile mismatch suppression in websocket delivery
  - unseeded library -> availability rows but zero release events; seed marker flips behavior
  - per-series burst cap: N+5 events for one series -> N fanned out, 5 suppressed with `suppressed_reason`
  - cross-library dedupe: same episode released in two libraries -> one delivery per profile
  - outbox recovery: deliveries committed with `pending` attempt rows and no dispatch -> recovery worker sends them
- [ ] API tests for:
  - inbox list scoping to active profile
  - unread count
  - mark-read idempotency
  - preferences read/write if included
- [ ] Manual verification:
  - favorite a show
  - add a new episode
  - confirm one `release_event`, one `notification_delivery`, one websocket event, one unread badge increment
  - repeat scan and confirm no duplicate delivery
  - switch profiles and confirm unread counts and inbox rows change correctly
- [ ] Load verification:
  - simulate a popular series with many interested profiles
  - confirm fanout batch time and insert counts remain bounded
  - confirm ingest completion time does not materially regress when release events are enabled

---

## Metrics and Observability

- `release_events_created_total`
- `release_events_processed_total`
- `release_events_suppressed_total` (burst cap; labeled by reason)
- `notification_outbox_recovered_total` (pending attempt rows claimed by recovery instead of the inline dispatcher)
- `notification_availability_seeded_total`
- `notification_recipients_selected_total`
- `notification_deliveries_inserted_total`
- `notification_deliveries_deduped_total`
- `notification_fanout_duration_ms`
- `notification_ws_publish_failures_total`
- `notification_repair_runs_total`
- `notification_interest_rebuild_rows_total`
- `notification_unread_count_queries_total`
- `notification_preferences_updates_total`

Log structured attributes:
- `library_id`
- `series_id`
- `episode_id`
- `recipient_count`
- `inserted_count`
- `deduped_count`
- `duration_ms`
- `profile_id` for API and websocket logs where appropriate

---

## Risks and Safeguards

- **Risk:** fanout leaks notifications across profiles.
  - **Safeguard:** carry both `user_id` and `profile_id` on deliveries and websocket events; enforce profile filtering in handlers.

- **Risk:** rescans emit duplicate notifications.
  - **Safeguard:** dedupe at both `episode_availability` and `notification_deliveries`.

- **Risk:** profile interest rows drift from source-of-truth user state.
  - **Safeguard:** backfill and repair tasks, plus idempotent updaters on every source mutation.

- **Risk:** ingest slows down due to notification work.
  - **Safeguard:** ingest only records availability and release events; fanout is entirely asynchronous.

- **Risk:** feature ships with incomplete interest state and under-notifies.
  - **Safeguard:** run the backfill before enabling fanout and keep a repair task available.

- **Risk:** notification tables grow without bound.
  - **Safeguard:** define retention up front and implement cleanup tasks.

- **Risk:** a partial worker failure marks events processed too early.
  - **Safeguard:** process deliveries and processed-state updates in one transaction.

- **Risk:** a crash between delivery commit and channel dispatch silently loses pushes/webhooks.
  - **Safeguard:** the outbox — `pending` per-target attempt rows committed in the fanout transaction; recovery workers sweep stale pending rows.

- **Risk:** a back-catalog import floods inboxes and the push relay on a server with hundreds of users.
  - **Safeguard:** availability seeding for new libraries and feature enablement; per-series burst cap with logged suppression for bulk additions to existing libraries.

- **Risk:** dual-quality libraries ("TV" + "TV 4K") double-notify every release.
  - **Safeguard:** partial unique index on `(profile_id, episode_id)` for `episode.available` deliveries.

---

## Defaults and Assumptions

- V1 covers episodes only.
- V1 stores one delivery per new episode per profile.
- Aggregated phrasing like "3 new episodes available" is a presentation or v2 delivery concern.
- Availability is determined by actual in-library episode presence, not future calendar metadata.
- Existing `catalog` websocket invalidation remains separate from the new notification channel.
- Preference defaults are all-enabled unless a profile has explicitly saved overrides.
- Home-surface dismissals are independent from notification suppression.

---

## Launch Checklist

- [ ] Migrations applied successfully.
- [ ] Interest backfill task deployed.
- [ ] Interest backfill completed or reached acceptable coverage.
- [ ] Availability seeding completed; every library has a seed marker.
- [ ] Release event creation verified on a staging ingest, including: adding a brand-new library emits zero events; adding a back-catalog season to an existing library emits events but fanout is capped.
- [ ] Fanout worker enabled in staging and dedupe validated with repeated scans.
- [ ] Inbox APIs validated with multiple profiles on the same user.
- [ ] Realtime events validated with multiple tabs and profile switching.
- [ ] Retention/cleanup path scheduled or documented.
- [ ] Metrics visible in logs or dashboards before production rollout.
