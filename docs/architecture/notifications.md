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

The detector also stores that decision on the availability row: an episode's
`episode_availability.released_at` is set when it was recorded as a release
(seeded library, inside the window), and stays empty for back catalog and late
matches. A profile that turns on `ui.next_up_new_releases_first` (off by
default) gets Next Up ranked by the later of when it finished a series' previous episode and the `released_at` of
the next episode (earliest availability row in the libraries the viewer can
reach), so a caught-up series moves to the front when a new episode or season
arrives. Changing what counts as a release changes that order too.

Event kinds:

- `episode` — carries series/episode identity and fans out to interested
  profiles. An unset kind is normalized to `episode` (the column defaults to
  it; only in-memory events can be empty).
- Flat item kinds (`movie`, `audiobook`, `ebook`) — carry an item ID only and
  feed the server-channel broadcast feed; they do not fan out per profile
  except as watchlist arrivals (see [Watchlist arrivals](#watchlist-arrivals)).

An event can be suppressed instead of fanned out, with the reason recorded on
the row. Fanout writes one reason, `stale`: the event aged past the fanout
staleness horizon (72 hours; extended downtime, or no node running fanout), and
delivering it long after the fact would be noise. Older rows can also carry
`series_burst`, written by a per-series burst cap that profile fanout no
longer has; the value stays readable (`SuppressedReasonSeriesBurst`). A season
pack needs no cap: the Next Up rule under "Eligibility rules" lets each
profile hear about at most one episode of it.

Server channels read the event feed directly, suppressed events included, and
skip a title that a different, still existing library had made available
before the event: a second copy (a 4K library beside an HD one) is not news
for a server-wide post. Profile fanout keeps those events, because it must
still reach profiles that can see only the library that got the copy. It
skips a profile that can open the episode through another library that
holds it now and made it available before the event, so the copy is not news
to that profile. The check reads current access and current copies, not
what the profile was told: a profile that can open the earlier copy is
skipped even if that copy's own event never reached it (a suppressed event,
or access granted later). Once the earlier library's copy is
gone, the check no longer skips the profile, though the delivery dedupe key
below still holds the copy back while the first delivery is in the inbox.

The delivery `type` registry (`episode.available`, `webhook.auto_disabled`,
`request.*`, …) is extensible by construction; clients must render unknown
types with a generic fallback.

## Eligibility rules

A profile gets an `episode.available` delivery for one thing: the next
episode after what it has watched arrives. That is the card Home's Next Up row
shows for the series, with one difference: an episode the catalog lists but
the library has no file for yet holds its place, so a later episode arriving
first does not notify. Continue Watching, favorites and the watchlist do not
notify on their own; a favorited or watchlisted series notifies only through
Next Up. The rule
covers every per-profile channel (inbox, realtime, web push, mobile push,
email, Discord DM, per-profile webhooks). Server channels are outside it: they
read release events directly, with no profile and no reason flags. Request
notifications have their own rules (see "Request notifications").

`EvaluateRecipient` (`internal/notifications/fanout_logic.go`) creates a
delivery for event E (series S, episode key K) only when all of these hold:

- The profile's master toggle (`enabled`) and `notify_next_up` are on.
- The profile's interest row for S has `next_up_candidate` set and a cursor,
  `next_expected_episode_key`.
- E is the first listed episode at or after the cursor: `cursor <= K`, and
  the catalog lists no episode of S, with or without a file, with a key in
  `[cursor, K)`. It cannot be key equality: a key is
  `season * 1_000_000 + episode`, so a season premiere never equals the
  finale's key + 1, and skipped episode numbers would never match either.
- `last_notified_episode_key` is unset or below K.
- The series is not muted and the profile can open the episode (both
  below).

"Listed" means an `episodes` row of the series in any library, whether or not
it has a file. Metadata lists a season's episodes before their files arrive,
and those rows are what hold a place. The fanout worker reads
`HighestListedEpisodeKeyBelow` (`release_repo.go`) once per event, and only for
an event that has candidates, so the third condition is
`cursor > highestBelow`. Candidates that fail the row-only part
(`nextUpMatches`) are dropped before their preferences are read, so a bulk
import costs one lookup per event, not per candidate. Both fanout passes
(capture, then evaluation under the inbox locks) reuse the same value.

What follows from the rule:

- A profile caught up through E05 gets one delivery when E06, E07 and E08
  arrive together: E06. E07 and E08 fail the third condition because E06 is
  listed below them. A season pack therefore needs no per-series cap.
- If the profile does not watch E06, E09 arriving a week later does not
  notify. Nothing notifies until the profile watches on.
- After finishing S01E10, the finale, the profile is told about S02E01.
- Out-of-order arrival waits for the listed episode. When the catalog lists
  E06 and E07 arrives days before E06's file, E07 stays silent although Home
  shows it as Next Up, and E06 notifies when it lands. A listed episode that
  never arrives keeps the series silent until the profile watches past it.
- When the catalog lists nothing at or after the cursor (a finale before next
  season's metadata exists, or metadata that trails the files), the first
  episode to arrive there notifies. If E07 arrives before E06 is listed at
  all, E07 notifies, and E06 arriving later does not:
  `last_notified_episode_key` is already E07.
- The rule reads the catalog when fanout runs. One ingest records all its
  events in one transaction, so episodes linked by one scan are all listed by
  the time any of them is evaluated, whatever order the scan linked them in.
  For separate imports, fanout waits 30 seconds before it can claim each
  event. If E07 arrives first and the catalog lists E06 by the time fanout
  evaluates E07, E07 stays silent and E06 can notify when its file is
  available. If E06's file arrives during the delay but the catalog still
  has no entry for E06 when fanout evaluates E07, E07 can notify.

Fanout never moves the cursor. It changes only when the interest recompute
runs, which profile activity queues (see "Next Up cursor" below).

The other gates:

- A series the profile muted is a hard gate too. `ListActiveBySeries`, the
  query that loads a release's candidate recipients, anti-joins
  `profile_series_notification_mutes`, so a muting profile is never a
  candidate: no delivery row is created and no channel sees the event. The
  check costs one index probe per candidate inside that query, and it runs
  again when fanout re-reads the candidates under the inbox locks. A mute
  committed after that second read is like a preference change made just
  after the delivery. Muting writes no interest row: the mute lives in its own
  table keyed by account, profile, and series, because interest rows are
  recomputed per library and pruned once their reasons go, and a mute lasts
  until the profile unmutes. The account is part of the key because profile
  IDs, such as the legacy `default`, repeat across accounts. A muted
  profile's notification cursor does not move. The mute is read when fanout
  evaluates a release, not when the episode arrives: unmuting does not
  announce episodes fanout already evaluated during the mute, since their
  release events are processed, but an event still inside the settle delay at
  unmute is fanned out to the profile. No suppression state is kept per
  release; the window is the settle delay plus one claim pass, and it applies
  symmetrically to a mute made just after a release. A mute follows its series through a
  catalog merge or split like other per-profile series state, and profile
  deletion removes it.
- Profile-level notification preferences are a hard gate: with `enabled` or
  `notify_next_up` off, no delivery row is created and no channel, webhooks
  included, sees the event. A webhook's `notify_next_up` can only narrow
  further, never re-enable.
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

An `episode.available` delivery's `reason_flags` is `{"next_up": true}`.
Older inbox rows can also carry `favorite`, `watchlist` and
`continue_watching` keys: `parseReasonFlags` ignores them, and the inbox API
returns the stored object as written. Deliveries are deduplicated per
`(account, profile, release event)` and, for `episode.available`, per `(account, profile, episode)` across libraries.
Retention removes those rows, so once the first delivery is gone, profile
fanout's earlier-copy check (see "Release types") is what keeps a profile
that can still open the earlier copy from being told again.
Reprocessing an event is idempotent.

A profile ID is unique only within its account: every account from before
profiles has a profile named `default`. Interest rows, preferences, mutes,
the delivery dedupe keys, the email settings, and every inbox read, count,
and mark-read are therefore keyed by account and profile, and profile
deletion removes those for the deleted profile's account only. A verified
email address still serves one profile on the whole server. Still keyed by
profile ID alone: the inbox clock (`notification_inbox_clocks`), which only
orders inserts, so two accounts' same-ID profiles share it and serialize
their inserts without changing either inbox's order; and push devices, web
push subscriptions, and webhooks.

The notified-series list (`GET /api/v2/notifications/series`,
`System.ListNotifiedSeries`) shows a profile the interest rows that have a
cursor, plus the series it muted. A series in several libraries is one entry;
its cursor is the lowest among the rows that are Next Up candidates.
`ActiveReasons` is the event-independent half of these rules, shared by
fanout and the list, so the two cannot disagree about whether preferences
silence Next Up. The list also says whether the profile is caught up: the
episode it waits for has not arrived, meaning the first listed episode at or
after the cursor has no present file (a `media_files` row with `missing_since`
unset, `presentEpisodeFileSQL`), or none is listed. A series notifies
(`NotifiedSeries.Notifies`) when it is not muted, Next Up is active, and the
profile is caught up; otherwise the episode it waits for is already in the
library and nothing notifies until it watches on. A muted
series keeps its active reasons on the list but does not notify. The list
filters series through the catalog's card access check rather than fanout's
per-library one, and it leaves out the already-notified cursor, which depends
on the episode that arrives.

### Next Up cursor

`InterestUpdater.RecomputeSeries` (`interest_updater.go`) computes
`next_expected_episode_key` and `next_up_candidate` for one profile and
series:

1. **Anchor.** The anchor is Home's: the most recently completed episode by
   progress time, ties going to the later episode. Specials (season 0) have
   no airing position, so when the anchor is a special in a series with
   regular seasons, it moves to the most recently completed regular episode,
   or to just before season 1 when the profile has finished none (catalog's
   `nextUpAnchorsCTE`). A profile with completed watch history but no
   completed progress row (a history import, a watch-provider sync) has no
   Home anchor; it anchors on the highest completed episode instead. Home
   shows no Next Up card for such a series, but notifications still follow
   it. This departure from Home is deliberate.
2. **Walk past started episodes.** The cursor starts at anchor + 1 and moves
   past every listed episode the profile has started, stopping at the first
   listed episode it has not started, whether or not that episode has a file
   (`nextUpCursor` in `home_hides.go`). "Started" is a progress row that is
   completed or has a position, or a completed history entry. A Postgres
   progress row counts even when a history hide covers it, as on Home, and an
   episode key counts as started when any row with that key is. The first listed episode
   at or after the cursor is therefore the one the profile waits for. When
   every listed episode after the anchor is started, the cursor sits after
   the last of them. Without the walk, a rewatcher would never hear about a
   new season: caught up through S05 and rewatching S01E01, the anchor is
   S01E01, and a cursor of S01E02 would wait on an episode that is already
   available.
3. **Partly watched.** `next_up_candidate` is false while the series has an
   episode in progress (a position, not completed) whose progress is newer
   than the anchor completion. This is Home's Next Up test
   (`inProgressExclusion` in `nextup_repo.go`), special-anchor case included,
   implemented by `partlyWatchedSinceAnchor`. Once a special anchor has
   moved, a regular episode is measured against the regular completion it
   moved to. With the history-only anchor, any episode in progress counts.
4. **Home removals.** `next_up_candidate` is also false for a series the
   profile removed from Next Up:
   - An active series drop clears it until the profile watches the series
     again.
   - A per-card Next Up dismissal clears it while the dismissed episode is
     still the card Home would show: the first episode after the anchor that
     has a present file and that the profile has not started (by Home's
     Postgres progress test, or by the profile's own store and completed
     history).

   Continue Watching dismissals do not affect notifications, and the
   dismissal hooks queue no recompute for them.

The cursor is kept while `next_up_candidate` is false, so Next Up resumes
from the right episode once a removal lapses or the profile finishes the
episode. A row with neither a candidate flag nor a completed episode can never
notify, and the recompute deletes it.

Watch progress, watch history and Next Up dismissals queue a recompute
through the notification-wrapped user store (`WrapUserStoreProvider` in
`interest_hooks.go`), and series drops through `DroppedSeriesTracker`
(`home_hides.go`). Favorite and watchlist changes queue none. Every removal or
restore queues a recompute, and a second one ten minutes later. Resuming
playback can lift a drop without changing any progress state, so a progress
write queues one too when it is the row's first write since the profile's
last Home change on this node, or when it lands more than ten minutes after
the row's stamp, by its own stamp or by the clock (a new watch session, or a
late import). Finishing an episode counts as a Home change here: it moves the
anchor, so resuming an older partly watched episode right afterwards queues a
recompute, which takes the series off Next Up as Home does.

The Home-change marker and the second recompute live in the memory of the
node that handled the removal. A resume handled by another node within ten
minutes waits for that second recompute, and if the node restarts first, for
the daily interest rebuild. A release fanned out in that window is judged on
the stale interest, and it is not judged again: fanout marks the event
processed, so a profile the stale row left out is never told about that
episode. Evaluating Home removals at fanout time would close the gap.

### Upgrade

Migration `20261116231355_drop_retired_notification_reasons` drops
`profile_series_interest.favorite`, `watchlist` and `continue_watching`,
`notification_preferences.notify_favorites`, `notify_watchlist` and
`notify_continue_watching`, and the same three `notify_*` columns on
`notification_webhooks`. It also deletes the interest rows only a removed
reason kept, clears `last_notified_episode_key` where the catalog lists an episode between
the cursor and the mark (the old rules notified later episodes, and the mark
would silence the one the profile waits for), rebuilds `profile_series_interest_active_idx` on
`next_up_candidate`, and deletes the `notifications.fanout.max_series_burst`
setting.

This migration requires a maintenance window. Binaries from before it read and
write the dropped columns, so stop every API replica and its background jobs
before a new replica starts migrations, and do not restart an old replica
against the migrated database. A rolling deployment is unsafe. Release notes
for a build containing this migration must carry the requirement. This follows
the [backup and rollback policy](../release-versioning.md#backups-and-rollback)
and the [cluster cutover policy](api-contract.md#cluster-cutover-policy).

Interest rows written before the upgrade keep their old cursor (last completed
episode + 1) until the daily interest rebuild or the profile's next activity
recomputes them. Under the new rule that cursor behaves like a current one for
a profile that is caught up.

## Release-event retention

Retention runs daily with fixed windows: read deliveries are kept 90 days,
unread ones 180 days, processed release events 30 days, and unprocessed
events past the 72-hour staleness horizon are deleted.

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
the title since no catalog item exists yet) rather than the `next_up`
reason. Partial unique indexes make the inserts idempotent:
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

## Watchlist arrivals

A profile that watchlisted a movie or series before the library had it is told
when it arrives, whether or not anyone requested it: a profile that turned off
watchlist requests, or a server without requests, still hears. Fanout handles
it from release events, in the same transaction as episode deliveries
(`watchlist_arrival.go`). A fresh movie event names the movie; a fresh episode
event names its series. The item counts as arrived only when its
`media_items.created_at` is within the fanout staleness horizon, so a series
arrives with its first episodes and its later episodes are no arrival. The
catalog row's creation stands in for the first file: a scan creates the row
when it finds the file. A row created ahead of its file, as a catalog seed
import does, is therefore no arrival once it is older than the horizon, and
an entry added after the row exists was not waiting. Judging arrival by the
first `movie_availability` or `episode_availability` row instead would cover
those titles.

For each arrived item, `watchlist.Arrivals.WaitingFor` finds two kinds of
waiting profile:

- An unpromoted `user_watchlist_titles` entry, added before the item's
  `created_at`, whose title resolves to the item the way promotion resolves it
  (`catalog.ResolveProviderAliases`: any provider ID the title holds or once
  held, an ID set that does not contradict the item's, an item in an enabled
  library).
- An entry promotion already moved onto the library watchlist. A profile that
  reads its watchlist between the arrival and the fanout (the settle delay)
  promotes its entry first. The library watchlist can live in the per-user
  SQLite store, which fanout cannot query, so promotion records the move in
  `watchlist_title_promotions` in its own transaction, keeping the entry's
  `added_at` and every TMDB ID the title held, so a request made under a
  former ID is still found. A record counts when that `added_at` predates the item's
  `created_at`. Notification retention deletes records promoted before the
  staleness horizon, which can no longer match; deleting a profile deletes
  its records, and so does taking the item off the library watchlist, by
  any path, through the notification system's user store wrapper
  (`interest_hooks.go`).

The lookups run on the fanout transaction, and each item's waiters are checked
in one batch. Each is told unless:

- its notifications are off (the master switch; there is no per-type switch),
- it may not open the item (the access check episodes use, against any
  library),
- a request of the title will tell it: it requested or follows an active
  request whose fulfilled notice has not gone out, under any TMDB ID the
  title held (`requests.Repository.AwaitingFulfillment`), or
- a `request.fulfilled` for the title already reached it.

So a profile that both requested and watchlisted a title hears once, from the
request. The delivery is `watchlist.available`, with the arrived item in
`series_id` (as `request.fulfilled` names it) and the title's TMDB ID and
media type (`movie` or `series`) in `reason_flags`. The partial unique index
`notification_deliveries_account_profile_watchlist_key` keeps one per
`(account, profile, item)`. Webhooks and plugin destinations carry it under
`notify_requests`; web
push, mobile push, email and Discord messages carry it like any delivery.

Arrival rides on release events, so it inherits their gaps: a library's silent
seeding scan and events past the staleness horizon tell nobody. Delivery
retention keeps a `watchlist.available` row until it is older than the
staleness horizon, whatever the retention settings, so a later episode
inside the horizon cannot tell the profile again.

## Outbound webhooks

Each profile can register up to a capped number of webhook destinations
(Discord or generic). Each has two event switches: `notify_next_up` for
`episode.available` deliveries (`Webhook.MatchesReasons`) and
`notify_requests` for request notices and watchlist arrivals. The profile
chose the
URL, so notification content — titles, season/episode numbers — is included;
the guardrails below are the ones the profile cannot opt out of.

### Trust model and SSRF guard

Webhook deliveries are direct outbound HTTP from the user's own server, so
the destination URL is attacker-controllable input against that server's
network position:

- HTTPS only; TLS verification is not user-overridable.
- By default, the destination host must never resolve to a private or
  special-purpose address. The deny set covers the IPv4 private/special ranges (loopback,
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
  exists for development environments. It permits the shared Private class;
  Blocked addresses remain refused at registration and every connection.
  Registration in either mode requires every resolved address to be allowed;
  lookup failure, an empty answer, or the two-second lookup deadline rejects
  the destination. Public-only requests also refuse the server's own interface
  addresses, as required by the shared policy. The delivery client keeps
  one connection pool per value of the setting and picks one from its current
  value on every request, so turning it off also stops deliveries over
  keep-alive connections opened while it was on.

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
`username: "Silo"` and an accent color: one for a Next Up episode, and a
default for an embed with no reason, such as a request notice. An episode
embed carries one reason label, "Next Up". The builder enforces Discord's
embed limits — 256-char title, 4096-char description, 1024-char field
values, 2048-char footer, 6000 chars total — truncating description first
and never the title. Embeds cannot ping: Discord only parses mentions from
the top-level `content` field, which this payload never sets (the
`allowed_mentions` field is omitted rather than sent).

Discord applies the 6000-char total to a whole message, not to each embed.
Multi-embed messages (Discord DM digests and server-channel content posts, up
to 10 embeds) count characters across all their embeds: overview teasers
shorten first, then the oldest embeds move into the "…and N more" `content`
line, which also counts items past the 10-embed cap.

### Generic payload and HMAC signing

Generic webhooks receive canonical Silo JSON: event name, delivery and
webhook IDs, timestamp, `version`, `test` flag, profile ID, library ID
(left out of request notifications), delivery type, `reason_flags`, and the
series/episode (or request) content blocks.
`reason_flags` is `{"next_up": <bool>}`; Next Up is the only reason an
episode notifies for. A `watchlist.available` payload names the arrived item
in the series block and the watchlisted title in a `watchlist` block,
`{"tmdb_id", "media_type"}` (`movie` or `series`; `tmdb_id` is left out when
the title has none), and its `reason_flags` read `{"next_up": false}`.

Each request is signed with the webhook's per-destination signing secret: 32
random bytes, base64-encoded into the 44-character string the API returns.
The HMAC key is that string's bytes as returned, not the decoded 32 bytes.

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
destination-side WAF/CDN blips intermittently 4xx valid webhooks. The webhook
row counts that streak (`consecutive_rejections`); a success, re-enabling, or
a new URL resets it. On a 429, a `Retry-After` header can lengthen the wait
before the next attempt but never shortens the schedule; other statuses ignore
it. Auto-disable posts a
`webhook.auto_disabled` notice to the profile's inbox — a type that is itself
excluded from webhook dispatch so a broken webhook cannot loop. The disable
and its notice commit with the attempt's outcome and the streaks, and only
the worker that turns the webhook off writes the notice.

A claimed attempt carries a 90-second lease. Only the worker that still
holds the claim records the send's outcome, so a send that outlived its lease
and was reclaimed counts once. A worker starts a send only with at least 25
seconds of lease left (up to 10 seconds of artwork resolution, the 10-second
request, and slack), and retry sweeps claim no more attempts than fit in one
lease.

Inbox retention keeps a delivery while one of its webhook or plugin
destination attempts is still pending or retrying: deleting the delivery
would cascade to the attempt and cancel a retry still on its schedule. It
keeps it always while the attempt is claimed or scheduled (`retrying` with
`next_retry_at` ahead), and otherwise only while the delivery is younger than
the plugin attempt age cap (36 hours, `pluginAttemptMaxAge`), so a stranded
attempt (webhooks turned off server-wide, for example) releases its row.

### Revisions and guarded deletion

`notification_webhooks.revision` is a server-owned bigint allocated from a
non-cycling PostgreSQL sequence (migration
`20260906145619_webhook_conditional_revision`). The insert/update trigger
assigns it for every write, including configuration updates, signing-secret
rotation, delivery success/failure bookkeeping, and disabling a destination. It
does not depend on transaction timestamps. Gaps are expected, and a recreated
row receives a new revision even if its ID or supplied revision repeats.
Sequence exhaustion fails writes rather than reusing a validator. Every writer
goes through the trigger; trigger bypass, a manual sequence reset and
independently restored divergent databases are outside this guarantee.

The repository's guarded deletion seam locks the profile-owned row, invokes the
caller's precondition check on its current revision, and deletes within the
same transaction. A mismatch returns without deleting the row or cascading its
attempts. Missing and foreign-profile rows report not found. Concurrent writers
serialize through the same row lock; an editor that wins first makes the
original deletion revision stale. A writer that loses to a committed deletion
cannot update that deleted row.

The revision covers operational metadata as well as configuration, so a
provider outcome may invalidate an editor's validator. This is conservative
lost-update protection, not a configuration-only version. A database deletion
cannot recall an external request already sent. The `/api/v2` transport owns
validator rendering and conditional-header evaluation; the storage package does
not interpret HTTP headers.

## Plugin destinations

A plugin's `notification_channel.v1` capability provides a channel that
profiles add destinations on; see the SDK's `docs/notification-channel.md` for the plugin
contract. The host keeps everything except the send: which deliveries a
destination gets, the outbox, retries, failure counts and auto-disable. The
code is `internal/notifications/plugin_destination_*.go`; the plugin runtime
is reached through the `PluginChannelHost` interface, which `cmd/silo` wires
to the plugin service, so neither package imports the other.

- **Rows.** `notification_plugin_destinations` names a channel by `plugin_id`
  and `capability_id`, which survive plugin upgrades and reinstalls. Settings
  are one `enc:v1:` envelope bound to the row id
  (`notification_plugin_destination:<id>:config`) holding
  `{"settings": {...}, "write_only": [...]}`. Secret fields, classified by
  `plugins.ConfigSchemaFieldSets` as for plugin configuration and by each
  form key exactly as written (never trimmed), are never returned; a write
  that leaves one blank keeps the stored value. `write_only` records which
  keys were saved as secret, and they stay write-only (never returned,
  listed in `secret_settings_set`) whatever a later manifest says, until the
  owner replaces or clears them, so a plugin upgrade that drops a secret
  marking can't expose a credential. Redaction of plugin text counts them as
  secret too. Settings stored without a `write_only` array (a bare settings
  object, or an envelope that names the record differently) don't say which
  values were secret, so every value is write-only. The owner's next
  settings save writes the record: a value it replaces is classified by the
  schema then current, and every value it leaves alone stays write-only.
  Only fields
  the current schema classifies as public are returned, so a stored field a
  plugin upgrade stopped declaring stays hidden. Names are unique per account
  profile. Restoring a backup carries live destinations and webhooks back by
  id in one upsert on the backup's own schema; the restored rows it will
  update first move to a name no live row can hold (a space and the id), so
  names swapped since the backup don't collide part way through.
- **Flat forms.** A destination form is one object of scalar fields; the
  SDK's `manifest.ValidateDestinationSchema` refuses anything else, and plugin
  install and upgrade run it through `manifest.Validate`. One rule decides
  which channels exist, for listing, connecting and queueing alike
  (`plugins` `notificationChannelsOf`): a stored capability that doesn't
  decode, whose form fails that check, or whose JSON Schema doesn't compile
  is skipped, so a record from before the rule reads as not provided instead
  of reaching every client. A save that meets a schema that doesn't compile
  answers as for a channel not provided, never a retryable `503`. The API
  sends clients `PluginChannel.FormFields`: one flat field list with each
  field's control, value type, secret flag, default, select options and the
  checks below, never the JSON Schema or the admin form descriptor.
- **Settings checks.** Before the plugin's `ValidateDestination` sees them,
  the host checks merged settings against the form's JSON Schema with the
  SDK's `config.ValidateValue`, as for plugin configuration: declared fields
  only, each of its declared scalar type, required fields present (a stored
  secret left blank counts), `enum`, string `minLength`/`maxLength`/`pattern`
  and numeric `minimum`/`maximum`. Each failing field is a `422` field error
  under `body.settings.<field>`. Anything else is the plugin's to check.
- **Installations.** A channel is named by plugin and capability ID, and
  direct archive installs can leave two enabled installations of one plugin.
  `plugins.NotificationChannels` lists each channel once, from the oldest
  enabled installation that provides it, and `NotificationChannelClientFor`
  calls that same installation, so settings are checked against the schema
  the client was shown and sent to the plugin that declared it. A channel no
  enabled installation provides is `ErrPluginChannelNotProvided`, which the
  API answers with `422` on create and `409` on update: waiting won't fix
  it. A plugin that provides the channel but fails to answer is
  `ErrPluginChannelUnavailable` alone, a `503` with `Retry-After`. Listing,
  connecting and queueing each read the enabled installations' capability
  records and check each channel's schema again; the plugin service has an
  installation cache but none for capabilities, and one would need its own
  cross-server invalidation, so that cost stays per call.
- **Revision.** `revision` guards the owner's edits and backs the ETag. Owner
  edits and auto-disable bump it; recording a send's success or failure
  doesn't, so a send finishing never turns an edit in progress into a `412`.
  A send's success, failure count and disable apply only while the
  destination is at the revision that was sent.
- **Edits.** The plugin's `ValidateDestination` can take 10 seconds, so an
  update runs it before opening the write transaction, against the revision
  it read. The write then locks the row and applies only while the revision
  is unchanged; otherwise it answers `412` and writes nothing. No row lock is
  held across a plugin call. A settings edit on a server that can't decrypt
  the stored settings (a wrong at-rest key) answers `409` and changes
  nothing, since merging over nothing would drop the stored secrets. An edit
  of name, `enabled` or the two switches neither reads nor rewrites the
  settings, so it goes through on such a server. The edit's row lock is
  `FOR NO KEY UPDATE`, which doesn't wait for a delivery's key-share lock.
- **Create and deletion.** Profiles live in the user store, PostgreSQL or the
  per-account SQLite store, so a create checks the profile through it, once
  before the insert and again after the insert commits, deleting the new
  destination if the profile is gone by then. A profile deletion that commits
  after that second check purges the profile's destinations afterwards, this
  one included. The insert key-share locks only the account row, the row an
  account deletion locks before its cascade reaches the profile and its
  destinations, so the two wait on each other instead of deadlocking. A
  profile purge locks the profile's destinations, and its webhooks, web push
  subscriptions and push registrations, in id order before deleting them, the
  order an enqueue locks them.
- **Enqueue.** Like webhooks, attempts are enqueued in the fanout
  transaction, filtered by the destination's switch for the delivery
  (`notify_next_up` for an episode, `notify_requests` for a watchlist
  arrival) and a per-profile limit of 30 a minute, and in
  `DispatchOperational` when `PluginDestinationFilter` selects them (request
  updates use `notify_requests`). As for webhooks, the limit is counted in
  memory on each API server, so a profile can get up to 30 a minute from each
  server that fans out for it, and operational notices such as request
  updates bypass it. The insert queues only for destinations that still
  exist and key-share locks them, in id order, until the delivery commits, so a
  destination deleted after it was listed drops its attempt instead of
  aborting the delivery, inbox row and other channels included, on the
  foreign key; webhooks, web push and mobile push enqueue the same way. The
  retention sweeps of web push subscriptions and push registrations skip a
  row a delivery holds instead of waiting on it, which could deadlock with an
  enqueue, and take it on a later run. The sweep of push registrations with no
  recorded login then deletes each row it skipped on its own, waiting for the
  delivery, since its Apple evidence, an ended login, can be gone by the next
  run; a statement that locks one row can't deadlock with an enqueue. All of a
  transaction's attempts go in one `INSERT ... SELECT FROM unnest(...)` with
  four parameters, so a
  fanout of any size stays under the protocol's parameter limit. A delivery
  the insert's `RETURNING` names is marked (`InsertedDelivery.pluginAttempts`),
  and only those reach the plugin destination dispatcher, so other deliveries
  cost no claim query; the retry sweep picks up any missed hand-off.
  Nothing is enqueued until the plugin runtime is wired,
  or for a destination whose channel no enabled plugin provides: it lists as
  unavailable and gets nothing until its plugin is back. Availability is the
  channel listing's own rule (`plugins.ListNotificationChannels`), read
  through the same transaction, so it is never stale and the transaction
  holds no second pool connection.
- **Send.** `plugin_destination_delivery_attempts` follows the webhook
  outbox: a claim lease, a sweep for due retries and stranded pending rows,
  and the webhook retry schedule. A claim holds at most 10 attempts and the
  lease is 5 minutes. Reaching the plugin, starting it when needed, has 10
  seconds (a slower cold start goes on without the attempt, which retries)
  and the send 15, so a whole claim finishes inside the lease. Just before
  the send RPC the server checks that its claim still holds with the send's
  time left on it, and skips the send otherwise; then it reads the
  destination again: one turned off or deleted meanwhile gets nothing (the
  attempt closes as `failed`, with no failure counted), and one edited
  meanwhile is sent its new settings. Finishing an attempt checks the lease
  it was claimed with, so a server whose lease ran out never overwrites a
  newer claim's result. The host renders the message with
  `BuildNotificationDisplay`, makes its link absolute with the email link
  base, adds a poster only under the Discord poster mode, and sends the
  delivery itself in the public `notification.created` shape, with every
  identifier a string (`events.Notification`).
- **Outcomes.** A plugin's answer and the call's errors map as follows.
  - Delivered (or an empty response): the attempt is `delivered` and the
    failure streak resets.
  - `RETRY`, and any error not listed below: a failed attempt that walks the
    schedule, with `retry_after_seconds` (capped at 6 hours) as a minimum wait
    that can push an attempt later than the schedule, never earlier. That
    includes the send's own 15-second timeout, and a `DeadlineExceeded` or
    `Canceled` status the plugin returned.
  - `FAILED`: the message is dropped; the destination stays as it is.
  - `DESTINATION_INVALID`: the message is dropped and the destination is
    disabled at once.
  - Plugin unavailable, meaning the host couldn't reach the plugin: no
    enabled installation provides the channel, the client wasn't acquired
    within its 10 seconds, the connection failed or closed (an error that
    isn't a gRPC status), or the transport answered gRPC `Unavailable`. It
    retries on the schedule; when the schedule runs out the message is
    dropped and the destination is not disabled.
  - Settings this server can't decrypt (a wrong at-rest key): retried, and on
    exhaustion the message is dropped without disabling, so one misconfigured
    server can't turn off every destination.
  - Any other exhausted schedule drops the message, and disables the
    destination only when it had no successful send (`last_success_at`) and
    no owner edit (`updated_at`) since the message was queued, so one stuck
    message, or one that failed before its owner fixed the settings, doesn't
    turn off a destination that works; the attempt then reads `failed`, not
    `auto_disabled`.
  - Age cap: whatever its attempt count, an attempt whose delivery is older
    than the schedule's span and half again (36 hours for its 24,
    `pluginAttemptMaxAge`) is finished without a send, so `retrying` rows
    can't pile up when the single retry sweep falls behind. Long
    `retry_after_seconds` waits can reach the cap before the schedule runs
    out, so the cap applies the exhausted schedule's rule: when the
    attempt's last stored failure was an ordinary one and at least three
    sends were made (`agedDisableMinAttempts`), the destination is disabled
    if it stayed quiet since the message was queued. After a
    plugin-unavailable or decrypt failure, or with fewer sends (the time
    went to a server or sweep that wasn't running), it is not. The attempt
    stores only its last failure's message, so those two kinds are told
    apart by the host's fixed text for them, which is stored unmasked.
- **Finishing.** The attempt's outcome, the failure count, the disable and
  the `plugin_destination.auto_disabled` notice's inbox row commit in one
  transaction, and the notice, which reaches no webhook or plugin
  destination, reaches live clients after commit. Everything the notice
  needs from outside that transaction (which channels the administrator has
  on) is read before it begins, so it never waits for a second pool
  connection.
- **Plugin text.** Text a plugin writes (send results, refusals, errors)
  goes through one function before it is stored, logged or returned. It is
  made storable first: valid UTF-8 without NUL or other control characters
  except newline and tab, at most 256 characters, so PostgreSQL can't refuse
  it and resend a message forever. Then every value of the destination's
  settings that the schema doesn't declare public is masked, which covers
  secrets and stored fields the schema dropped, as written and, for values
  of 8 bytes or more, in base64 (standard and URL alphabets, padded or not),
  URL-escaped and hex form; then `logredact.SanitizeText` masks credential
  assignments and URL userinfo.
- **Retention.** Inbox retention keeps a delivery while a webhook or plugin
  attempt of it can still run: always while the attempt is claimed or
  scheduled (`retrying` with `next_retry_at` ahead, which covers a live
  lease), and otherwise while the delivery is younger than the age cap.
- **Multi-node.** Any API server may claim an attempt and calls its own
  plugin process. When a server dies mid-send, its claimed attempts stay
  hidden until their 5-minute lease runs out, then another server's sweep
  sends them again; a message the dead server had already handed to its
  plugin can then arrive twice. The request's `delivery_key` is the same on
  every attempt so a plugin can deduplicate. Each server runs one sweep that
  sends its claims one after another, so a slow plugin, up to 25 seconds an
  attempt, delays other plugins' retries on that server; the webhook retry
  worker behaves the same way.
- **Network.** The host's webhook SSRF guard doesn't cover plugin requests.
  A plugin must not let a destination's settings, which non-administrators
  fill in, pick the host it connects to.

