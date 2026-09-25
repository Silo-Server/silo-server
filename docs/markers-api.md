# Marker API

The v2 marker API reads effective file markers and updates manual markers. Each
operation applies the existing file, library, item, episode-parent, and extra-parent
access policy. Reads require authentication. Writes also require `marker_edit`
permission and pass the demo and viewer-access gates. Profile context remains
optional; supplying a profile applies its access restrictions.

## Provider modes and storage

New installations include TheIntroDB and default to online markers with local
detection as a fallback (`markers.mode=both`). Online markers are saved to the
library by default. The setup wizard offers separate controls for online lookup
and local detection before automatic lookup begins. New TV and mixed libraries
created in the web UI enable local detection by default; existing library choices
are preserved. Local detection only runs in libraries where it is enabled.
Existing installations retain their configured marker mode, which accepts `off`,
`local`, `online`, and `both`.

`markers.online_storage` chooses how online results are used:

- `stored` persists markers and enables the **Sync online markers** task,
  scheduled daily at 03:00 in server-local time by default.
  Sync checks unqueried files before refreshing previous results. Successful
  responses are fresh for seven days; empty results are retried after one day.
  `markers.lazy_playback` also allows reads and playback to fill missing markers.
- `on_demand` looks up markers for the selected file and keeps a bounded,
  fifteen-minute memory cache. It does not persist provider responses or run
  background synchronization. Request leases, failures, and quota cooldowns are
  still shared through the database. Previously stored markers remain available.

Both paths honor provider priority, manual edits, and provider quota limits.

`markers.detection_workers` sizes local detection: how many seasons the
**Detect markers on this server** task analyzes at once, which also bounds how
many ffmpeg processes read audio. It defaults to `1` and accepts 1 to 64.
Detection mostly waits on reading each file's opening minutes, so a higher
value finishes a large library sooner on fast storage, at the cost of load
that competes with playback. Analysis started from playback always has one
extra ffmpeg slot of its own. A change applies without a restart; extractions
already running finish first.

Replicas coordinate fetches with expiring database leases. File replacement or
rematching invalidates derived markers; a result fetched for the previous file
identity cannot overwrite the new one. Successful provider refreshes can correct
or withdraw that provider's existing ranges.

`POST /api/v2/admin/items/{id}/refresh-markers` explicitly refreshes an episode
from its configured sources. In `both` mode, eligible local detection fills
missing intro markers. The existing v1 refresh endpoint retains its
local-only behavior.

## Operations

| Method | Path | Operation |
| --- | --- | --- |
| GET | `/api/v2/markers/files/{file_id}` | `getFileMarkers` |
| GET | `/api/v2/markers/items/{item_id}` | `getItemMarkers` |
| PUT | `/api/v2/markers/files/{file_id}` | `setFileMarkers` |
| PUT | `/api/v2/markers/items/{item_id}` | `setItemMarkers` |
| DELETE | `/api/v2/markers/files/{file_id}/{segment}` | `clearFileMarkerSegment` |

Item routes select the item's first accessible file, using episode files before
content files. Every success returns HTTP 200 with a string `file_id` and four
objects: `intro`, `credits`, `recap`, and `preview`. A segment with no marker is an
empty object. Present boundaries are `start_seconds` and `end_seconds`, measured
in source-file seconds. Provenance fields (`source`, `provider`, `confidence`,
`algorithm`, `detected_at`) are omitted when absent. Timestamps use the shared v2
instant format.

Responses also include `marker_segments`, an array of every effective marker
occurrence in source-time order. Each entry has `kind` (`intro`, `credits`,
`recap`, or `preview`), `start_seconds`, and `end_seconds`. The array is empty
when no markers exist. Several entries may have the same kind; clients must
treat them as separate ranges and must not skip the gaps between them. The
four singular objects remain available as compatibility projections. Files
with only legacy markers contribute those ranges to the collection.

The playback capability `marker_segments_v1` advertises collection support.
The same collection appears on each file version in v2 watch detail. Reads may
populate markers after access checks according to the configured provider mode;
provider failures preserve the existing readable marker state.

PUT accepts a partial update: omit a segment to leave it unchanged, send `null`
to clear it, or send an object to set it. For example:

```json
{"intro":{"end_seconds":48.5},"credits":null}
```

This sets intro from zero to 48.5 seconds, clears credits, and preserves recap and
preview. Intro and recap default a missing start to zero and require an end.
Credits and preview require a start and default a missing end to the known file
duration. Individual boundaries cannot be null. Unknown fields, negative or
nonfinite boundaries, and an end at or before the start are rejected. Existing
duration validation retains its one-second tolerance; an unknown duration cannot
supply a default end.

The existing manual fields address one kind at a time. Setting one replaces
that kind's occurrences with the supplied range; clearing it removes every
occurrence of that kind. Other kinds are unchanged. `marker_segments` is a
read-only projection, not a PUT field.

All supplied segments are validated before the shared writer commits the mixed
set/clear update and its audit rows in one transaction. Audit identity comes from
authenticated claims. Failed validation or audit insertion rolls back the update.
After commit, the service reloads the effective markers, publishes a notification,
and may contribute newly set segments through the existing provider service.

Writes advertise `non_retryable`. Database no-op detection and contribution
claims do not guarantee exactly-once external effects: notifications may repeat,
and a provider can accept a contribution before its response or local receipt is
lost. A failed response can therefore follow a successful save. Read the current
markers before deciding whether another user-directed update is needed.

The legacy v1 adapter shares the manual writer path and retains its wire format.
Jellyfin does not expose these manual editing routes. Its MediaSegments response
returns each occurrence separately, with credits represented as `Outro`.
The generated OpenAPI document is the authoritative v2 schema and error contract.
