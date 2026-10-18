# Progress bootstrap API

The v2 bootstrap protocol returns a durable, immutable **full replacement** of
one profile's server progress. It does not provide incremental deltas. Ordinary
`GET /api/v2/progress` remains browse pagination, and
`POST /api/v2/sync/progress` remains a separate upload operation.

Every bootstrap operation requires the existing account/profile authentication
and PIN gates. The service rechecks the current credential, account, profile,
effective policy and generation. Responses use `Cache-Control: no-store`.

| Operation | Method and path | Success |
| --- | --- | --- |
| getProgressBootstrapCapabilities | GET `/api/v2/sync/progress/capabilities` | 200 capability document |
| createProgressBootstrapSnapshot | POST `/api/v2/sync/progress/snapshots` | 201 first page and canonical Location |
| getProgressBootstrapSnapshot | GET `/api/v2/sync/progress/snapshots/{snapshot_id}?cursor=…` | 200 next page |

Capability state is `available` only for the selected account's PostgreSQL store
on the central authority database with the production policy service configured.
SQLite reports `unsupported`, with `allowed=false` and no installation/generation
identity. Missing configuration or schema reports `not_configured`; dependency
outages return a safe 503 Problem. Capability availability does not enable the
SQLite importer, switch a provider, or claim native clients have adopted bootstrap.
The document includes `mode="full_replace"`, `incremental=false`, installation
and generation identifiers when available, and the admission limits below.

## Admission and continuation

Create accepts `{ "request_id": "<UUID>", "limit": 200 }`. Limit is optional,
defaults to 200, and must be between 1 and 200. Generate a request ID once for an
admission intent and persist it with the staged replacement. Repeating it within
the retention horizon returns the same first page, without recapturing state or
extending expiry. Changing its limit returns `snapshot_request_conflict`.
A new request ID means a new snapshot and consumes another admission slot.

Each page contains `snapshot_id`, `installation_id`, `account_id`, `profile_id`,
`generation`, `mode`, `captured_at`, `expires_at`, stored `item_count`, `items`,
`page`, and `complete`. Account/profile IDs are strings. Items use the existing
progress projection: `media_item_id`, `position_seconds`, `duration_seconds`,
`completed`, and `updated_at`. Completion false and position zero are real values.

Follow only `page.next_cursor`, keeping the same snapshot path and captured
account/profile. Tokens bind the operation, replacement mode, installation,
account/profile, access digest, generation, snapshot, page size, ordinal and
expiry. Clients cannot choose an offset or pass a legacy numeric `since` value.
A terminal page has `complete=true`, `page.has_more=false`, no next cursor, and a
`completion_token`. That token is a signed completion receipt, never an upload
acknowledgement, continuation cursor, or incremental checkpoint. An empty terminal
snapshot is a valid instruction to clear the matching server-derived cache.

Admission is synchronous and bounded to 30 seconds, 100,000 visible rows, and
64 MiB of retained normalized JSON payload. There are at most two active snapshots
per account. Snapshots expire after 15 minutes. Expired payloads are removed by
bounded startup/minute cleanup; metadata remains for 24 more hours to distinguish
expired retries from new requests. Retry safety is finite: after that metadata
horizon the server may admit a reused request ID anew. Clients must start a fresh
intent and staging cache after expiry rather than relying on indefinite replay.

## Failures and client obligations

Existing 401/403 authentication and PIN failures apply. Missing or foreign
snapshots return 404 before cursor parsing. Malformed, tampered, wrong-operation,
or mismatched cursors return 400 `invalid_cursor`. Known expired snapshots and
changed generation/access return 409 `sync_reset_required`; start a new intent.
Changed admission input returns 409 `snapshot_request_conflict`. Admission bounds
return 413 `progress_snapshot_too_large`, never a truncated successful result.
Quota exhaustion returns 429 with `Retry-After: 30`. Unavailable dependencies
return 503 without database errors or source values.

Stage every page separately from the visible cache. Check captured authority after
each asynchronous operation and atomically publish only a complete replacement
for that same installation/account/profile/generation. Keep the previous cache
when staging fails or expires. Preserve downloads and the offline event queue;
completion receipts acknowledge neither. Do not OR previous completion state into
a replacement. Upload acknowledgements remove only the exact submitted event IDs,
never events appended while an upload was in flight.

Apple and Android adoption remains coordinated follow-up: persisted staging and
request ID, atomic replacement, completed-to-incomplete handling, empty replacement,
interruption/restart recovery, identity-switch cancellation, and offline-queue
preservation must be verified before backend conversion can be activated.
Jellyfin-compatible browsing and existing progress uploads retain their behavior.

Storage and transaction invariants are described in
[progress bootstrap storage](architecture/progress-bootstrap.md).

## Clearing a resume position

`clearResumePosition` (DELETE `/api/v2/progress/{media_item_id}/position`)
clears the acting profile's resume position on a movie or episode and answers
204. It is the manual edit for an item someone skimmed and does not want left
partly watched. The progress entry stays with `position_seconds` 0 and keeps
`completed` and `duration_seconds`, so a rewatched item stays watched. Watch
history is untouched. Mark unwatched (`unmarkWatched`) is different: it removes
the item's history, including unfinished plays.

The write is stamped with the server time. It advances the delta-sync cursor, so
other devices receive the cleared position, and an older provider resume point
cannot restore it. No watch-provider event is sent, so a provider that kept a
resume point from an earlier scrobble keeps it. Like mark unwatched, clearing an
episode of a [dropped series](architecture/dropped-shows.md) ends the drop, and
watch sync then lifts it on providers that sync drops. When a position was
cleared, the server publishes `user_state.changed` with `change: "progress"`.

An item without a resume position is a no-op 204. A season, series or ebook has
no single resume position and fails validation at `path.media_item_id` (422). An
unknown or hidden item is 404, as is any other kind `markWatched` does not
handle, such as an audiobook. The operation is profile scoped and
`non_retryable`: a delayed retry would also clear a position recorded after the
first request.

Clients detect the operation with `getProgressCapabilities`
(GET `/api/v2/progress/capabilities`), a profile-scoped capability document
whose `clear_resume_position` is true when it is served. Jellyfin clients
already have the equivalent: `PlaybackPositionTicks` 0 without `Played` on
`/UserItems/{itemId}/UserData` ([Jellyfin compatibility](jellycompat-api.md)).

## Playback-origin uploads

`POST /api/v2/sync/progress` and its v1 counterpart upload playback positions,
including client-owned audiobook timelines and offline queues. Neither
`force_overwrite` nor `updated_at` carries playback authority. Genuine manual
watch-state edits and trusted imports use separate operations and retain their
existing behavior.

A multi-part audiobook starts with `progress_persistence: "client"`, so the
playback session records no resume position of its own. The client owns the
timeline and reports the global book position through
`POST /api/v2/sync/progress`. Session progress and stop
([playback API](playback-api.md)) still sequence the session itself; they do not
carry the book position for these attempts.
