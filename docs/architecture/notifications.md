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

Availability is recorded when a file links to an episode or item, which can
happen long after the file arrived: a file can sit in the library unmatched
until a parser fix or metadata correction. Content added more than 14 days
before it became available is therefore recorded without a release event. The added time is the catalog's: `episode_libraries.first_seen_at`
(taken from the earliest linked file) for episodes, and the earliest present
file in the library for flat items. The window leaves room for new episodes
whose metadata lands a few days after the file.

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

Server channels read the event feed directly, suppressed events included, and
skip a title that a different, still existing library had made available
before the event: a second copy (a 4K library beside an HD one) is not news
for a server-wide post. Profile fanout keeps those events. It already deduplicates per episode
across libraries, and it must still reach profiles that can see only the
library that got the copy.

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
- `continue_watching` and `next_up` follow Home. The interest recompute
  clears them for a series the profile removed from that surface:
  - An active series drop clears both until the profile watches the series
    again.
  - A per-card Continue Watching dismissal clears `continue_watching` while
    the dismissed episode's progress is unchanged. As on Home, an in-progress
    episode no longer counts once a later episode of the series was completed
    more recently.
  - A per-card Next Up dismissal clears `next_up` while the dismissed episode
    is still the card Home would show: the first episode after the most
    recently completed one that has a present file and that the profile has
    not started (by Home's Postgres progress test, or by the profile's own
    store).

  Favorites and watchlist are unaffected. The progression cursor is kept, so
  `next_up` resumes from the right episode once a removal lapses. Every
  removal or restore queues a recompute, and a second one ten minutes later.
  Resuming playback can lift a removal without changing any progress state,
  so a progress write queues one too when it is the row's first write since
  the profile's last Home change on this node, or when it lands more than
  ten minutes after the row's stamp, by its own stamp or by the clock (a new
  watch session, or a late import).

  The Home-change marker and the second recompute live in the memory of the
  node that handled the removal. A resume handled by another node within ten
  minutes waits for that second recompute, and if the node restarts first,
  for the daily interest rebuild. A release fanned out in that window is
  judged on the stale interest. Evaluating Home removals at fanout time would
  close the gap.
- Profile-level notification preferences are a hard gate: a reason disabled
  in preferences can never match, so no delivery row is created and no
  channel — including webhooks — ever sees the event. Per-webhook reason
  filters can only narrow further, never re-enable.
- Access is the other hard gate, checked when fanout runs. The candidate's
  scope is resolved the way a catalog request resolves it, the event's
  library must be in that scope, and the episode must pass the catalog's
  visibility query: series library membership, content-rating ceiling and
  advisory-age limit. Interest rows are recomputed when the profile's
  relationship to the series changes and by the daily rebuild, and the
  recompute checks libraries only, so a row can outlive a library
  restriction for up to a day and a maturity limit indefinitely; this check
  is what keeps the title off every channel for that profile. A candidate
  whose profile no longer exists is skipped, and its notification cursor
  does not move. If a scope cannot be resolved right now (a failed read, or
  viewer preferences that could not be loaded), the batch rolls back and the
  event is retried on the next run. Only candidates that would otherwise get
  a delivery are resolved. A delivery already in an inbox is not re-checked
  when access changes later.

Multiple matching reasons produce one delivery with merged reason flags.
Deliveries are deduplicated per `(profile, release event)` and, for
`episode.available`, per `(profile, episode)` across libraries, so
dual-quality library setups notify at most once. Reprocessing an event is
idempotent.

## Release-event retention

Release events and inbox deliveries have independent lifetimes. Pruning an old
release event clears `notification_deliveries.release_event_id` through its
`ON DELETE SET NULL` foreign key and retains the delivery. An `episode.available`
delivery still requires its `library_id`, `series_id` and `episode_id` snapshot;
its event reference is optional. Event pruning must not delete inbox rows or
relax those snapshot requirements.

The constraint repair preserves existing deliveries. Its rollback restores the
previous constraint only if every episode delivery still has an event reference;
otherwise PostgreSQL rejects the rollback atomically. It never deletes detached
inbox rows to make rollback possible.

## Request notifications

Request lifecycle deliveries (`request.fulfilled`, `request.approved`,
`request.declined`) are operational notices posted directly to the requesting
profile — no interest index, no fanout. `request.fulfilled` also goes to every
profile that followed the title (see
[media-requests.md](media-requests.md#following-a-title)), with `follower: true`
in its `reason_flags` so every channel words it as a followed title rather than
the recipient's own request. Their `reason_flags` carry request
identifiers (request ID, TMDB ID, media type; approved/declined also carry
the title since no catalog item exists yet) rather than the four reason
booleans. Partial unique indexes make the inserts idempotent:
`request.fulfilled` per `(user_id, profile_id, request_id)`, because a
follower on another account can share the requester's profile id (every
account from before profiles has a `default` profile), and approved/declined,
which only reach the requester, per `(profile_id, request_id, type)`. An
operational delivery's webhook, web push and mobile push targets are the
recipient profile's on the recipient's account. The per-webhook
`notify_requests` flag gates the webhook channel for them.

An operational delivery that names a catalog item (`request.fulfilled`,
through `series_id`) is created only when the recipient can open that item,
checked as fanout checks an episode. A requester or follower without access
is skipped, and the request still counts as notified. A scope that cannot be
resolved fails that recipient's dispatch and the request is retried; the
other recipients are still told, and the retry skips every recipient that
already has its delivery. The scope is resolved before the dispatch
transaction opens, because the resolver reads through the connection pool. `request.approved` and `request.declined` name no catalog
item; they repeat the title the requester submitted.

Approval is the one transition whose two destinations disagree. Server
channels see `request.approved` for every approval; the requester only gets a
`request.approved` delivery when an administrator approved a pending request.
Auto-approval is the policy answering the requester's own submission, so a
notice about it is noise. The approval paths that notify say who approved with
a `requests.ApprovalOrigin`, and the requester notice requires
`ApprovalOriginAdmin` explicitly: any other origin is skipped, and an
unrecognized one is logged, so a new policy-driven path cannot reintroduce the
notice by omission.

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
  and the IPv6 equivalents (unspecified, loopback, ULA, site-local, link-local,
  documentation, NAT64, multicast). IPv4-mapped IPv6 addresses are unwrapped
  before checking so `::ffff:127.0.0.1` cannot bypass the IPv4 entries. The
  address classes are shared with every other user-supplied destination; see
  [Outbound address guard](outbound-address-guard.md).
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
