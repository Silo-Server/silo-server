# Downloads & Offline Sync API (client integration guide)

> **API lifecycle:** this documents the frozen alpha `/api/v1` surface. Silo serves it through one
> pre-1.0 bridge release and then retires it; Silo 1.0's stable native API is `/api/v2`. See
> [the native API contract](architecture/api-contract.md).

This is the client-facing integration guide for downloads v2 / offline sync. It is
the contract the Apple (`silo-apple`) and Android (`silo-android`) apps should use
to download movies and episodes for fully offline playback and reconcile watch
state after reconnect.

It documents the current HTTP contract implemented by this server. Server-side
design rationale is summarized in [section 14](#14-design-notes-server-internals);
the implementation lives in `internal/downloads`.

> All endpoints are under `/api/v1`. Examples use `https://your-server` as the origin.

---

## 1. Concepts

Downloads v2 has three pillars:

1. **Device-scoped download registry.** The server tracks what each device has
   registered, what media file was selected, whether an artifact is still preparing,
   and whether the client confirmed local completion.
2. **Offline playback manifest.** One stable bundle per download containing metadata,
   artwork references, subtitle references, chapters, markers, media stream details,
   and integrity metadata. It contains no presigned or expiring URLs.
3. **Offline progress reconciliation.** Clients queue progress writes while offline,
   flush them when online, then pull server-ordered deltas made by other devices.

### Two download row lifecycles

The `/downloads` family serves two lifecycles. The presence of
`X-Silo-Device-Id` selects the managed path.

|                                 | Ephemeral / web row          | Managed device entry               |
| ------------------------------- | ---------------------------- | ---------------------------------- |
| Selected by                     | No `X-Silo-Device-Id` header | `X-Silo-Device-Id` header present  |
| Scope                           | Account (`user_id`)          | `(user_id, profile_id, device_id)` |
| Durable "device has this file"? | No                           | Yes                                |
| Manifest / artwork / subtitles  | Not applicable               | Yes                                |
| Progress reconciliation target  | No                           | Yes                                |
| Intended clients                | Web convenience download     | Mobile / TV offline library        |

Mobile clients should always send `X-Silo-Device-Id` and operate on managed entries.

Ephemeral rows are one-shot convenience records: the server prunes them
automatically about 7 days after their last update. Managed device entries are
never auto-pruned.

### Quality vs delivery format

Clients request a **quality preset**. The server records the concrete
**delivery format** it produced.

| Public quality | Meaning                                                                                                                                            |
| -------------- | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `original`     | Prefer source quality. If device caps show the source cannot be delivered directly, the server may transparently prepare a compatibility artifact. |
| `20mbps`       | Single-file transcode capped at about 20 Mbps.                                                                                                     |
| `10mbps`       | Single-file transcode capped at about 10 Mbps.                                                                                                     |
| `5mbps`        | Single-file transcode capped at about 5 Mbps.                                                                                                      |
| `2mbps`        | Single-file transcode capped at about 2 Mbps.                                                                                                      |
| `1mbps`        | Single-file transcode capped at about 1 Mbps.                                                                                                      |

`remux` is **not** a public quality preset. It is an internal delivery format used
when `original` is requested but device caps show the source only needs container
or audio compatibility work. Rows expose both:

- `quality`: what the client requested.
- `effective_quality`: what the server actually delivered after compatibility fallback.
- `delivery_format`: `original`, `remux`, or `transcode`.
- `target_bitrate_kbps`: `0` for original/remux; bitrate cap for transcodes.

The ordered preset ladder is:

```
original > 20mbps > 10mbps > 5mbps > 2mbps > 1mbps
```

Series and season batch requests are original-quality only. If some episodes do
not have a local file, the batch response includes them in `skipped` rather than
failing the whole batch.

### Metadata included offline

Yes, manifests include metadata needed to make the offline item feel native:

- Title, year, overview, runtime, content rating, genres.
- Series, season, and episode context for episodes.
- Poster/backdrop thumbhashes and authenticated artwork proxy URLs for poster,
  backdrop, and logo when available.
- Chapters, intro/credits/recap/preview markers.
- External and downloaded subtitle fetch URLs plus known subtitle file sizes.
- Container, codecs, resolution, HDR, duration, selected audio track, and audio
  track inventory. For remux/transcode entries these describe the prepared
  artifact the file endpoint actually delivers (single audio track, target
  container/codecs), not the catalog source it was prepared from.
- Stable provider identity and integrity metadata for local validation/rescan recovery.

The client still needs to fetch artwork/subtitle bytes once while online and cache
them locally beside the media file and manifest.

### Key invariants

- **No DRM, expiry, or lease.** Already-downloaded files remain playable until the
  user deletes them. The server can revoke future serves, not reach into a device.
- **Device authority is the header only.** A `device_id` in body/query is ignored.
- **Every managed asset re-checks profile access.** A download id alone never grants
  content access.
- **Server-owned progress cursors.** `?since=` uses a server sequence, not a client
  timestamp. Client timestamps are only last-write-wins inputs for that profile.

---

## 2. Authentication & headers

All endpoints require authentication. Managed operations require a profile and a
device id.

| Header                               | Required when              | Notes                                                       |
| ------------------------------------ | -------------------------- | ----------------------------------------------------------- |
| `Authorization: Bearer <token>`      | Always                     | JWT access token or API key (`sa_...`).                     |
| `X-Profile-Id: <profile_id>`         | Managed ops, progress sync | Active household profile.                                   |
| `X-Silo-Device-Id: <device_id>`      | Managed downloads          | Stable per-install UUID; its presence selects managed mode. |
| `X-Silo-Device-Name: <name>`         | Optional                   | Display name, clamped server-side.                          |
| `X-Silo-Device-Platform: <platform>` | Optional                   | Example: `android`, `ios`, `tvos`.                          |

A managed call without `X-Silo-Device-Id` returns `400 device_id_required`; one
without profile scope returns `400 profile_required`.

On `/api/v2`, `X-Silo-Device-Id` must be sent exactly once and be a single device
identifier: letters, digits, `.`, `_`, `:` or `-`, with no comma or interior
whitespace (surrounding whitespace is ignored; each operation keeps its 128-character
bound). A repeated header line or a comma-joined value is refused with
`422 validation_failed` at `header.x-silo-device-id` before any operation runs, so a
joined value can never be stored as a device identity. v1 reads only the first header
line and is unchanged.

> **Warning:** any client that sends `X-Silo-Device-Id` on download routes MUST
> also send `X-Profile-Id`. A device header without a profile is rejected with
> `400 profile_required`. The first-party web client sends both headers globally.

---

## 3. Feature detection

Call this at login and after profile switch. Do not sniff server versions.

```http
GET /api/v1/downloads/capability
```

Response:

```json
{
  "enabled": true,
  "download_allowed": true,
  "quality_presets": [
    "original",
    "20mbps",
    "10mbps",
    "5mbps",
    "2mbps",
    "1mbps"
  ],
  "transcode_enabled": true,
  "transcode_user_allowed": true,
  "season_download": true,
  "series_monitoring": true,
  "monitoring_modes": ["all", "future", "latest_season", "specific_seasons"],
  "proxy_delivery": true
}
```

| Field                    | Meaning                                                                           |
| ------------------------ | --------------------------------------------------------------------------------- |
| `enabled`                | Downloads feature is enabled on the server.                                       |
| `download_allowed`       | This user may download at all.                                                    |
| `quality_presets`        | Ordered quality values this user may request now. Only offer values in this list. |
| `transcode_enabled`      | Server-level transcode-to-file gate.                                              |
| `transcode_user_allowed` | Per-user transcode-to-file permission.                                            |
| `season_download`        | Per-season batch downloads are available.                                         |
| `series_monitoring`      | Auto-download subscriptions are available.                                        |
| `monitoring_modes`       | Subscription modes the client may request.                                        |
| `proxy_delivery`         | Proxy-aware download routes are mounted (§4.11); redirects are per-request.        |

`quality_presets` is always an array — `[]` (never `null`) when downloads are
disabled or the user lacks download permission — so clients can rely on
`quality_presets.length === 0` meaning "downloads unavailable for this account."

If `enabled` or `download_allowed` is false, hide download actions.

---

## 4. Endpoint reference

### 4.1 Create a download

```http
POST /api/v1/downloads
```

Send `X-Silo-Device-Id` and `X-Profile-Id` for a managed entry.

Request body:

| Field           | Type   | Notes                                                                        |
| --------------- | ------ | ---------------------------------------------------------------------------- |
| `content_id`    | string | Required. Movie or series content id.                                        |
| `episode_id`    | string | Episode content id for an episode download.                                  |
| `file_id`       | int    | Optional explicit media-file/version id.                                     |
| `quality`       | string | `original` by default, or one of `quality_presets`.                          |
| `series`        | bool   | `true` means download every episode of `content_id` at original quality.     |
| `season_number` | int    | With `series: true`, restrict to one season. `0` is the Specials season; negative values are rejected with `400`. Dispatch is on field presence: omit the field entirely for a whole-series download. |
| `caps`          | object | Device decode capabilities. Important for `original` compatibility fallback. |

Capabilities mirror streaming playback caps:

```json
{
  "caps": {
    "codecs_video": ["h264", "hevc"],
    "codecs_audio": ["aac", "ac3"],
    "audio_passthrough_codecs": ["ac3", "eac3"],
    "containers": ["mp4", "mkv"],
    "max_resolution": "1080p",
    "hdr": false
  }
}
```

`caps` fields:

| Field                      | Type     | Notes                                                                                                   |
| -------------------------- | -------- | ------------------------------------------------------------------------------------------------------- |
| `codecs_video`             | string[] | Flat list of video codecs the device can decode.                                                        |
| `codecs_audio`             | string[] | Flat list of audio codecs the device can decode.                                                        |
| `audio_passthrough_codecs` | string[] | Codecs the connected sink accepts bit-exact.                                                            |
| `containers`               | string[] | Containers the device can open.                                                                         |
| `max_resolution`           | string   | Coarse device ceiling (`480p`…`2160p`). See the note under detailed evidence below.                     |
| `hdr`                      | bool     | Whether the display can present HDR.                                                                    |
| `client_features`          | string[] | Optional protocol-v3 feature tokens, e.g. `software_video_decode_v1`. Same vocabulary as playback start. |
| `video_evidence`           | string   | Optional provenance of the video facts: `declared`, `platform_attested`, or `exact`.                    |
| `video_decode`             | object[] | Optional per-decoder entries; same shape and bounds as the protocol-v3 `video_decode[]`.                |

The last three fields are additive and optional. They carry the same meaning as
on the v3 playback start request — see
[docs/architecture/playback-protocol-v3.md](architecture/playback-protocol-v3.md)
— and download creation accepts exactly the shapes playback accepts:

- Flat lists alone, with or without `video_evidence`, are always valid. A
  `declared` payload and a payload carrying only `client_features` both resolve
  from the flat codec lists.
- `video_decode` entries are only honoured at `video_evidence` of `exact` or
  `platform_attested`, because no weaker tier can validate them. Sending
  `video_decode` entries with `declared`, or with `video_evidence` omitted, is a
  partial opt-in the server will not silently ignore: it returns `400`
  `bad_request`. Malformed entries (empty `codec`, negative bounds, oversized
  lists) are rejected with `400` at any tier.

When a strict tier does supply entries, they decide whether a particular
original file is safe to hand over as-is, and they supersede the coarse
`max_resolution` ceiling — a `max_width: 3840` hardware entry preserves a 4K
original even when `max_resolution` says `1080p`. If the file's stored probe
metadata is too sparse to check against those bounds (missing bit depth,
dimensions, frame rate, or bitrate), the server falls back to the flat codec
lists rather than forcing a transcode of an original-quality download.

Single-item response (`202 Accepted`):

```json
{
  "id": "dl_01H...",
  "content_id": "mv_123",
  "media_file_id": 4567,
  "device_id": "device-uuid",
  "file_size": 8589934592,
  "bytes_sent": 0,
  "kind": "queued",
  "status": "ready",
  "quality": "original",
  "effective_quality": "original",
  "delivery_format": "original",
  "target_bitrate_kbps": 0,
  "revision": 1,
  "created_at": "2026-06-19T16:04:05Z"
}
```

Readiness behavior:

- Direct original rows are `ready` immediately.
- Compatibility remux or bitrate transcode rows are `preparing` until the artifact
  completes, then `ready`.
- If an equivalent artifact already exists, the row may be `ready` immediately.

Series/season response (`202 Accepted`):

```json
{
  "downloads": [
    {
      "id": "dl_...",
      "batch_id": "b_...",
      "content_id": "sr_123",
      "episode_id": "ep_1",
      "status": "ready",
      "quality": "original",
      "effective_quality": "original",
      "delivery_format": "original",
      "revision": 1
    }
  ],
  "skipped": [{ "episode_id": "ep_missing", "reason": "no_file" }]
}
```

Re-registering the same `(profile, device, content, episode)` is idempotent. If the
same entry is re-requested with a different quality or target, the server updates
the existing managed row, increments `revision`, and clients should replace the
local media + manifest for that row.

### 4.2 List downloads

```http
GET /api/v1/downloads
```

With `X-Silo-Device-Id`, returns that device's managed entries. Without it, returns
the user's ephemeral web rows.

Response:

```json
{
  "downloads": [
    /* download rows */
  ]
}
```

Use this to poll for `ready` and reconcile rows on app launch.

### 4.3 Confirm local state

```http
PATCH /api/v1/downloads/{id}
```

Managed-only. Body:

```json
{ "status": "downloading" }
```

or:

```json
{ "status": "completed" }
```

Returns `204 No Content`.

### 4.4 Delete a download

```http
DELETE /api/v1/downloads/{id}
```

Deletes the row owned by `(user, profile, header device)` and returns `204`. The
client is responsible for deleting local files.

### 4.5 Serve the media file

```http
GET /api/v1/downloads/{id}/file
HEAD /api/v1/downloads/{id}/file
```

Streams either the source file or the prepared artifact. Range requests are
supported for resumable/background downloads. `HEAD` is accepted like
`/direct-download`: it returns the same headers with no body so clients can
probe size and resumability before issuing ranged `GET`s.

Common responses:

- `200` or `206`: media bytes.
- `409 download_inactive`: row is revoked or otherwise not servable.
- `404 not_found`: row/content missing or outside profile access.
- A `preparing` artifact is not servable yet; wait for `ready`.

### 4.6 Offline manifest

```http
GET /api/v1/downloads/{id}/manifest
```

Managed-only. Fetch when the row reaches `ready`, store beside the media file, and
use it for offline playback UI.

### 4.7 Batch manifests

```http
GET /api/v1/downloads/batches/{batch_id}/manifests
```

Managed-only. Returns manifests for all ready/servable entries in a series or
season batch owned by the calling device.

```json
{
  "manifests": [
    /* OfflineManifest */
  ],
  "skipped": [{ "download_id": "dl_...", "reason": "not_found" }]
}
```

One unbuildable episode (deleted from the catalog, access-filtered, revoked)
does not fail the whole batch: it lands in `skipped` and the remaining
manifests are still delivered. `skipped` is omitted when empty.

| Reason      | Meaning                                                             |
| ----------- | -------------------------------------------------------------------- |
| `revoked`   | The row is revoked and no longer servable.                            |
| `not_found` | The row or its content is missing or outside profile access.          |
| `error`     | The server failed to build this manifest; safe to retry later.        |

Clients should drop or refresh local entries whose manifests come back
`not_found`.

Use this after a batch download if the client wants to fetch metadata for the
whole batch in one request.

### 4.8 Artwork proxy

```http
GET /api/v1/downloads/{id}/artwork/{kind}
```

`kind` is `poster`, `backdrop`, or `logo`. The manifest's `artwork_urls` point
here. Fetch each available image once while online and cache the bytes locally.

### 4.9 Subtitle proxy

```http
GET /api/v1/downloads/{id}/subtitles/{ref}
```

`ref` comes from `subtitles[].fetch_url` and encodes either `external:{index}` or
`downloaded:{id}`. Invalid refs return `400 invalid_subtitle_ref`.

### 4.10 Direct download

```http
GET /api/v1/direct-download?file_id={id}
HEAD /api/v1/direct-download?file_id={id}
```

Browser/web convenience path. It is synchronous and original-only. Mobile clients
should use managed `POST /downloads` plus `/downloads/{id}/file`.

For browser-friendly links, the endpoint accepts the session access token as a
`?token=` query parameter in place of the `Authorization` header.

> **Security note:** the query token is the session access token. Treat
> direct-download URLs as secrets — they end up in browser history and proxy
> logs. A short-lived download-scoped URL is a planned follow-up.

### 4.11 Distributed proxy delivery

`proxy_delivery` on the capability response reports whether the proxy-aware
routes exist:

```http
GET  /api/v1/downloads/{id}/file-proxy
HEAD /api/v1/downloads/{id}/file-proxy
GET  /api/v1/direct-download-proxy?file_id={id}
HEAD /api/v1/direct-download-proxy?file_id={id}
```

`true` means the routes are mounted, not that every request redirects. A
proxy-aware route returns `307` to a proxy node when one is eligible for that
file, and otherwise serves bytes directly with the same status-code contract as
the non-proxy route. Bandwidth-limited downloads (server-wide or per-user) are
never redirected, and neither are files no proxy node can reach. Clients must
follow the redirect or accept the direct response; they cannot assume either.

The established `/file` and `/direct-download` routes never redirect. When a
prepared artifact for `/downloads/{id}/file` lives on a transcode node, the API
relays it; the client sees an ordinary direct response. `/direct-download`
serves source files only and has no artifact case.

Treat proxy delivery as an advertised capability, not something inferred from a
server version.

---

## 5. Download row shape

| Field                 | Type   | Notes                                                                  |
| --------------------- | ------ | ---------------------------------------------------------------------- |
| `id`                  | string | Opaque download id.                                                    |
| `content_id`          | string | Movie or series id.                                                    |
| `episode_id`          | string | Present for episode rows.                                              |
| `batch_id`            | string | Present for series/season batch members.                               |
| `device_id`           | string | Present on managed entries.                                            |
| `media_file_id`       | int    | Selected media file/version.                                           |
| `file_size`           | int64  | Bytes; may be an estimate while preparing.                             |
| `bytes_sent`          | int64  | Set to `file_size` when an ephemeral row completes; not a live transfer counter. Managed rows report 0. |
| `kind`                | string | `direct` or `queued`.                                                  |
| `status`              | string | Lifecycle state.                                                       |
| `quality`             | string | Requested public quality.                                              |
| `effective_quality`   | string | Actual quality delivered after compatibility fallback.                 |
| `delivery_format`     | string | `original`, `remux`, or `transcode`.                                   |
| `target_bitrate_kbps` | int    | `0` for original/remux; bitrate cap for transcode.                     |
| `revision`            | int    | Increments when an existing managed row is replaced with a new target. |
| `created_at`          | string | RFC3339.                                                               |
| `completed_at`        | string | Present once completed.                                                |

Managed lifecycle:

```text
original:              ready -> downloading -> completed
compat/remux:          preparing -> ready -> downloading -> completed
bitrate/transcode:     preparing -> ready -> downloading -> completed
revoked:               any -> revoked (reserved)
failed artifact job:   preparing -> failed
```

Direct original rows are `ready` immediately; remux and transcode rows start at
`preparing` and become `ready` when the artifact completes. `failed` means the
artifact job exhausted its retries. `revoked` is reserved: nothing sets it
today, but an admin revoke flow is planned in a separate effort, so clients
must handle it. `downloading` and `completed` are set by the client via `PATCH`.

---

## 6. OfflineManifest shape

Manifests are stable and safe to persist offline.

```json
{
  "download_id": "dl_...",
  "content_id": "mv_123",
  "episode_id": "",
  "type": "movie",
  "revision": 1,
  "quality": "original",
  "effective_quality": "original",
  "delivery_format": "original",
  "target_bitrate_kbps": 0,
  "media_file_id": 4567,
  "file_size": 8589934592,

  "title": "Example Movie",
  "year": 2024,
  "overview": "...",
  "runtime": 7200,
  "content_rating": "PG-13",
  "genres": ["Drama"],
  "series_id": "",
  "series_title": "",
  "season_number": null,
  "episode_number": null,

  "poster_thumbhash": "iQ...",
  "backdrop_thumbhash": "iA...",
  "artwork_urls": {
    "poster": "/api/v1/downloads/dl_.../artwork/poster",
    "backdrop": "/api/v1/downloads/dl_.../artwork/backdrop",
    "logo": "/api/v1/downloads/dl_.../artwork/logo"
  },

  "container": "mp4",
  "codec_video": "h264",
  "codec_audio": "aac",
  "resolution": "1080p",
  "hdr": false,
  "duration_seconds": 7200,
  "selected_audio_track_index": 0,
  "audio_tracks": [
    {
      "index": 0,
      "language": "en",
      "codec": "aac",
      "channels": 6,
      "default": true
    }
  ],

  "chapters": [
    {
      "index": 0,
      "title": "Cold Open",
      "start_seconds": 0,
      "end_seconds": 142.5,
      "thumbnail_thumbhash": "iC..."
    }
  ],
  "intro": { "start": 60.0, "end": 90.0 },
  "credits": { "start": 7100.0, "end": 7200.0 },
  "recap": null,
  "preview": null,

  "subtitles": [
    {
      "language": "en",
      "format": "srt",
      "forced": false,
      "hearing_impaired": false,
      "external": true,
      "fetch_url": "/api/v1/downloads/dl_.../subtitles/external:0",
      "file_size": 41234
    }
  ],

  "stable_identity": {
    "stable_type": "movie",
    "provider_ids": { "tmdb": "12345", "imdb": "tt1234567" },
    "season": null,
    "episode": null
  },
  "integrity": {
    "expected_bytes": 8589934592,
    "media_file_hash": "sha256-or-scanner-hash",
    "metadata_etag": "opaque-server-value"
  },

  "manifest_version": 2,
  "generated_at": "2026-06-19T16:05:00Z"
}
```

Notes:

- Artwork and subtitle URLs are authenticated proxy paths on this server. Fetch
  them once while online and cache the bytes locally.
- Thumbhash fields are inline placeholders for fast offline UI rendering.
- `stable_identity` is for rescan recovery when a server-side `content_id` changes.
- `integrity.expected_bytes` should match the local media file size after download.
- `revision` should match the download row revision. If a row revision increases,
  refresh the media file and manifest.
- Optional fields are omitted when empty; clients should treat absent values as
  "not set."

---

## 7. Progress reconciliation

### 7.1 Flush queued progress

```http
POST /api/v1/sync/progress
```

Requires `X-Profile-Id`. Include `updated_at` for offline queued events.

```json
{
  "items": [
    {
      "media_item_id": "mv_123",
      "position": 1830.5,
      "duration": 7200,
      "updated_at": "2026-06-19T14:55:12Z"
    }
  ]
}
```

Response:

```json
{
  "results": [{ "media_item_id": "mv_123", "status": "ok" }]
}
```

Server behavior:

- `updated_at` is clamped to `server_now + 2m`. A malformed (non-RFC3339)
  `updated_at` fails that item with `updated_at must be RFC3339`; it is never
  treated as "now".
- Completion is calculated by server watched-threshold logic.
- Completed is a one-way latch; a lower later position does not unwatch an item.

### 7.2 Pull deltas

```http
GET /api/v1/progress?since={cursor}
```

Response:

```json
{
  "progress": [
    {
      "media_item_id": "mv_123",
      "position_seconds": 1830.5,
      "duration_seconds": 7200,
      "completed": false,
      "updated_at": "2026-06-19T14:55:12Z"
    }
  ],
  "next_cursor": "opaque"
}
```

Persist `next_cursor` and pass it as `since` next time. Treat it as opaque.

Row deletions (for example, dismissing an item from Continue Watching) do not
currently produce delta entries, so an offline device's cached resume point for
a deleted row goes stale until a full (cursor-less) refetch; clients should
treat the full snapshot as authoritative for removals.

---

## 8. Series monitoring

Series monitoring is a device-scoped opt-in to keep a series downloaded on this
device. It is client-driven: there is no server background worker. Call sync on
app open or background refresh, then pull rows from `GET /downloads`.

All subscription endpoints are managed-only.

### 8.1 Create

```http
POST /api/v1/downloads/subscriptions
```

```json
{
  "series_id": "sr_55",
  "mode": "latest_season",
  "delete_watched": true,
  "max_storage_bytes": 21474836480
}
```

| Field               | Type   | Notes                                                                               |
| ------------------- | ------ | ----------------------------------------------------------------------------------- |
| `series_id`         | string | Required.                                                                           |
| `mode`              | string | `all`, `future`, `latest_season`, or `specific_seasons`.                            |
| `season_numbers`    | int[]  | Required for `specific_seasons`.                                                    |
| `delete_watched`    | bool   | Client-enforced retention hint.                                                     |
| `max_storage_bytes` | int64  | `0` means unlimited. Client-enforced hard cap; server soft-gates auto-registration. |

Response:

```json
{
  "subscription": {
    /* subscription */
  },
  "registered": 12
}
```

### 8.2 Sync

```http
POST /api/v1/downloads/subscriptions/sync
```

Registers newly in-scope episodes across this device's subscriptions.

```json
{ "registered": 3 }
```

`registered` counts only episodes newly registered by this sync call; a
steady-state sync returns `{ "registered": 0 }`. Clients can skip refetching
`GET /downloads` when it is `0`.

### 8.3 List, get, update, delete

```http
GET    /api/v1/downloads/subscriptions
GET    /api/v1/downloads/subscriptions/{id}
PATCH  /api/v1/downloads/subscriptions/{id}
DELETE /api/v1/downloads/subscriptions/{id}
```

`PATCH` is partial:

```json
{ "mode": "specific_seasons", "season_numbers": [2, 3], "active": true }
```

Subscription shape:

```json
{
  "id": "sub_...",
  "series_id": "sr_55",
  "mode": "latest_season",
  "target_season": 4,
  "delete_watched": true,
  "max_storage_bytes": 21474836480,
  "active": true,
  "created_at": "2026-06-19T16:00:00Z",
  "updated_at": "2026-06-19T16:00:00Z"
}
```

Deleting a subscription stops future auto-registration. It does not delete local
files or existing download rows.

---

## 9. Recommended client strategy

### 9.1 Download one title

1. Call `GET /downloads/capability` and offer only `quality_presets`.
2. User picks Download: `POST /downloads` with `quality`, `caps`, profile, and device headers.
3. If the row is `preparing`, poll `GET /downloads` or listen on events (see 9.4) until `ready`.
4. Fetch and store `GET /downloads/{id}/manifest`.
5. Fetch and store all `artwork_urls` and `subtitles[].fetch_url` assets.
6. Download `GET /downloads/{id}/file` with Range/background support.
7. `PATCH /downloads/{id}` to `downloading` on start and `completed` on finish.
8. Play the local media file using the stored manifest.

### 9.2 Offline to online

1. While offline, queue `{media_item_id, position, duration, updated_at}` locally.
2. On reconnect, `POST /sync/progress`.
3. `GET /progress?since=<saved_cursor>` and save the returned `next_cursor`.
4. `POST /downloads/subscriptions/sync`.
5. If the sync response has `registered > 0`, `GET /downloads` to find the newly
   registered rows.

### 9.3 Robustness rules

- Re-check capability on profile switch.
- Keep already-downloaded files playable after `revoked` or `download_inactive`.
- Retry `POST /downloads`; registration is idempotent.
- If `revision` changes for an existing row, replace local media and manifest.
- Enforce subscription storage caps locally; the server only soft-gates.
- Treat `content_id` as rescan-sensitive; use `stable_identity` to recover.

### 9.4 Ready/failed push events

When an artifact completes or fails, the server publishes an event on the
existing user-state events channel (the SSE/WebSocket events hub), scoped to
the owning `(user, profile)`. The event type is `download` and the payload is:

```json
{
  "download_id": "dl_...",
  "status": "ready",
  "media_item_id": "mv_123",
  "format": "remux"
}
```

| Field           | Meaning                                                      |
| --------------- | ------------------------------------------------------------- |
| `download_id`   | The download row id.                                          |
| `status`        | `ready` or `failed`.                                          |
| `media_item_id` | The row's content id.                                         |
| `format`        | Delivery format: `original`, `remux`, or `transcode`.         |

Clients that hold an events connection can use this instead of polling
`GET /downloads` for `preparing` rows; polling remains the fallback.

---

## 10. Apple client implementation notes

This section is the handoff checklist for `silo-apple` across iOS, iPadOS, tvOS,
and macOS. Use the same HTTP contract above; these notes only pin the Apple-side
storage, background transfer, and playback choices.

### 10.1 Required local state

Persist these records in the app's local database:

| Local model            | Required fields                                                                                                                                                                                                                       |
| ---------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `OfflineDownload`      | `download_id`, `content_id`, `episode_id`, `batch_id`, `quality`, `effective_quality`, `delivery_format`, `target_bitrate_kbps`, `revision`, `status`, local media path, local manifest path, byte count, created/updated timestamps. |
| `OfflineAsset`         | `download_id`, asset kind (`media`, `poster`, `backdrop`, `logo`, `subtitle`), remote proxy path, local path, expected bytes if known, fetch status.                                                                                  |
| `OfflineProgressEvent` | `media_item_id`, `position`, `duration`, `updated_at`, retry/ack state.                                                                                                                                                               |
| `DownloadSubscription` | Server subscription id, `series_id`, mode, season filters, retention settings, active state.                                                                                                                                          |

Use the server `download_id` as the durable primary key for a downloaded item.
When a listed row has the same `download_id` but a larger `revision`, treat the
local media file, manifest, artwork, and subtitles as stale and re-fetch them.

### 10.2 Device identity and headers

Every managed request must include:

```http
Authorization: Bearer <access_token>
X-Profile-Id: <active_profile_id>
X-Silo-Device-Id: <stable_install_id>
X-Silo-Device-Name: <user_visible_device_name>
X-Silo-Device-Platform: ios
```

Recommended device id behavior:

- iOS/tvOS: use `UIDevice.identifierForVendor` when available, but persist the
  first value the app uses so the server sees a stable install id.
- macOS: generate a UUID once and persist it in the app's container/keychain.
- Do not send device id in JSON bodies or query strings; the server ignores it.

Use the platform value that matches the target (`ios`, `tvos`, or `macos`).

### 10.3 Capability and quality UI

On login, profile switch, and app foreground:

1. `GET /downloads/capability`.
2. Hide download actions unless `enabled && download_allowed`.
3. Offer only `quality_presets`, in the order returned by the server.
4. Label `original` as Original. Label bitrate presets as `20 Mbps`, `10 Mbps`,
   `5 Mbps`, `2 Mbps`, and `1 Mbps`.
5. Do not expose Remux. If the server chooses remux for compatibility, show that
   only in diagnostics/detail UI via `delivery_format`.

### 10.4 Suggested Apple decode caps

Send `caps` on create so `original` can fall back to a compatibility artifact
when needed. Start conservative and refine per device/OS if the Apple app already
has richer playback capability detection.

```json
{
  "caps": {
    "codecs_video": ["h264", "hevc"],
    "codecs_audio": ["aac", "ac3", "eac3"],
    "audio_passthrough_codecs": ["ac3", "eac3"],
    "containers": ["mp4", "mov", "m4v"],
    "max_resolution": "1080p",
    "hdr": false
  }
}
```

Use `max_resolution` and `hdr` from actual device/display capability where known.
For Apple TV 4K or modern HDR-capable devices, the client may advertise `4k` and
`hdr: true`; older phones/tablets should stay conservative. These caps affect
only server-side compatibility decisions and bitrate transcode targets.

A client with real decoder facts can send `video_evidence` and `video_decode`
alongside the flat lists. Note what that changes: detailed entries supersede the
coarse `max_resolution` ceiling, so a conservative `"max_resolution": "1080p"`
no longer bounds anything once `video_decode` describes a decoder that reaches
higher. If a hard 1080p cap is the intent, bound the entries themselves
(`max_width: 1920`, `max_height: 1080`, and the matching frame-rate and bitrate
limits) rather than relying on `max_resolution`.

```json
{
  "caps": {
    "client_features": ["software_video_decode_v1"],
    "video_evidence": "platform_attested",
    "codecs_video": ["h264", "hevc"],
    "codecs_audio": ["aac", "ac3", "eac3"],
    "audio_passthrough_codecs": ["ac3", "eac3"],
    "containers": ["mp4", "mov", "m4v"],
    "max_resolution": "1080p",
    "hdr": false,
    "video_decode": [
      {
        "codec": "hevc",
        "bit_depths": [8, 10],
        "max_width": 1920,
        "max_height": 1080,
        "max_frame_rate": 60,
        "max_bitrate_kbps": 40000,
        "hardware": true
      }
    ]
  }
}
```

### 10.5 Download orchestration

For a single movie or episode:

1. `POST /downloads` with `quality`, `caps`, and managed headers.
2. Store the returned row immediately.
3. If `status == "preparing"`, keep polling `GET /downloads` or consume server
   events until the row becomes `ready` or `failed`.
4. Once `ready`, fetch the manifest.
5. Queue artwork/subtitle asset downloads from the manifest.
6. Download `/downloads/{id}/file` with a background `URLSession`.
7. Patch `downloading` when the media transfer starts, and `completed` only after
   the media file and required manifest/assets have been moved into durable local
   storage.

For series or season download:

1. `POST /downloads` with `series: true`, optional `season_number`, and
   `quality: "original"`.
2. Persist each returned row under the shared `batch_id`.
3. Record `skipped` entries for user-visible diagnostics.
4. Fetch `GET /downloads/batches/{batch_id}/manifests` after rows are ready, or
   fetch individual manifests if the client is processing rows one at a time.
5. Handle `skipped` entries in the manifests response: drop or refresh local
   entries whose reason is `not_found`; retry later for `error`.

### 10.6 Background transfers

Use a background `URLSessionConfiguration` for media files so downloads can
continue across app suspension. Keep artwork and subtitles in the same queue or a
separate foreground queue; media bytes are the only large transfer.

Recommended transfer behavior:

- Always use the authenticated `/downloads/{id}/file` URL, not `direct-download`.
- Resume using HTTP Range support when the platform gives resume data.
- Move finished temporary files into the app's Application Support container.
- Avoid Caches for media and manifests; iOS may purge it.
- Mark local DB state after the file move succeeds, not when the transfer
  callback first fires.
- If auth expires before a queued background request starts, recreate the request
  with a fresh token and resume the transfer.

### 10.7 Local file layout

Suggested layout inside Application Support:

```text
OfflineDownloads/
  <download_id>/
    manifest.json
    media.mp4
    artwork/
      poster
      backdrop
      logo
    subtitles/
      external-0.srt
      downloaded-123.vtt
```

The media extension may be `.mp4` for prepared artifacts and may reflect the
source file extension for direct original delivery. The manifest's `container`
and the response `Content-Type` are better playback hints than the filename.

### 10.8 Offline playback

When offline, build the playback screen from `manifest.json` and play the local
media file URL with AVFoundation. Do not call server artwork/subtitle URLs during
offline playback; those URLs are fetch-once online proxy paths.

Use manifest fields as follows:

- Title/overview/year/rating/genres drive the detail header.
- `series_id`, `series_title`, `season_number`, and `episode_number` drive episode
  grouping.
- `poster_thumbhash` and `backdrop_thumbhash` are placeholders while local artwork
  bytes load.
- `chapters`, `intro`, `credits`, `recap`, and `preview` drive the same skip and
  chapter UI as online playback.
- `audio_tracks` and `selected_audio_track_index` seed the audio-track picker when
  the local player can expose matching tracks.
- External subtitles should be loaded from local cached subtitle files, not from
  `fetch_url`.

### 10.9 Offline progress sync

Queue progress locally whenever playback stops, pauses for a meaningful interval,
or crosses the watched threshold:

```json
{
  "media_item_id": "ep_88",
  "position": 120.0,
  "duration": 1500,
  "updated_at": "2026-06-19T14:55:12Z"
}
```

On reconnect:

1. `POST /sync/progress` with queued events.
2. Delete events acknowledged as `ok`.
3. `GET /progress?since=<saved_cursor>`.
4. Apply remote deltas to local resume state and save `next_cursor`.
5. `POST /downloads/subscriptions/sync`.
6. If the sync response has `registered > 0`, `GET /downloads` to register the
   new rows locally.

### 10.10 Retention and deletion

Deleting from the Apple offline library should:

1. Cancel any active `URLSessionTask` for that `download_id`.
2. Delete local media, manifest, artwork, and subtitle files.
3. Delete local DB rows.
4. Call `DELETE /downloads/{id}` while online, or queue that delete for the next
   reconnect.

If the server later returns `revoked` or `download_inactive`, keep existing local
files playable but stop retrying server fetches for that row.

---

## 11. Android client implementation notes

This section is the handoff checklist for `silo-android` across phone, tablet,
and Android TV. Use the same HTTP contract above; these notes only pin the
Android-side identity, storage, transfer, and playback choices. The required
local state mirrors the Apple table in 10.1.

### 11.1 Device identity and headers

Every managed request must include:

```http
Authorization: Bearer <access_token>
X-Profile-Id: <active_profile_id>
X-Silo-Device-Id: <stable_install_id>
X-Silo-Device-Name: <user_visible_device_name>
X-Silo-Device-Platform: android
```

Recommended device id behavior:

- Generate a UUID once on first launch and persist it in app-private storage
  (DataStore or equivalent); do not derive it from hardware identifiers.
- Remember the pairing rule from section 2: `X-Silo-Device-Id` without
  `X-Profile-Id` is rejected with `400 profile_required`. Attach both headers to
  every downloads call.
- Do not send device id in JSON bodies or query strings; the server ignores it.

### 11.2 Capability gating

On login, profile switch, and app start:

1. `GET /downloads/capability`.
2. Hide download actions unless `enabled && download_allowed`.
3. `quality_presets` is always an array; an empty array means downloads are
   unavailable for this account, so hide the downloads UI.
4. Offer only `quality_presets`, in the order returned, with the same labeling
   rules as 10.3 (Original plus `N Mbps`; never expose Remux).

### 11.3 Local storage

Persist the same records as 10.1 as Room tables: download rows (server fields
plus local paths and fetch status), per-download assets, queued progress events,
and subscriptions. Recommendations:

- Use the server `download_id` as the durable primary key. A larger `revision`
  for the same `download_id` marks local media, manifest, artwork, and subtitles
  stale.
- Store the manifest JSON verbatim beside the media file instead of exploding
  every field into columns; parse it at playback time.
- Keep media, manifests, and cached assets in app-internal storage (`filesDir`),
  never the cache directory, laid out per download id as in 10.7:

```text
offline_downloads/
  <download_id>/
    manifest.json
    media.mp4
    artwork/
      poster
      backdrop
      logo
    subtitles/
      external-0.srt
      downloaded-123.vtt
```

### 11.4 Download engine

Run media transfers as WorkManager-scheduled foreground work (a foreground
service with a progress notification), or the system `DownloadManager` if its
constraints fit the app:

1. `HEAD /downloads/{id}/file` first to probe size and resumability.
2. Download with ranged `GET`s; after process death or network loss, resume from
   the last persisted offset with a `Range` header.
3. Verify the final byte count against the manifest's `integrity.expected_bytes`
   before marking the row done locally.
4. Fetch artwork and subtitle assets once at download time from the manifest's
   `artwork_urls` and `subtitles[].fetch_url`.
5. `PATCH` `downloading` when the media transfer starts and `completed` only
   after the file and required assets are moved into durable storage.
6. If auth expires while a transfer is queued, recreate the request with a fresh
   token and resume.

### 11.5 Offline playback

Play the local media file with ExoPlayer (Media3) and build the detail and
playback UI from the stored `manifest.json`, following the same field mapping as
10.8:

- Side-load cached subtitle files as local subtitle tracks; never call
  `fetch_url` during offline playback.
- `chapters`, `intro`, `credits`, `recap`, and `preview` drive the same skip and
  chapter UI as online playback.
- Thumbhash fields are placeholders while local artwork bytes load.

### 11.6 Offline progress queue

Queue watch events in Room whenever playback stops, pauses for a meaningful
interval, or crosses the watched threshold, recording the client event time. On
reconnect:

1. `POST /sync/progress` with `updated_at` per item; delete events acknowledged
   as `ok`.
2. `GET /progress?since=<saved_cursor>` and persist `next_cursor` per profile.
3. Per the caveat in 7.2, row deletions produce no delta entries; periodically
   run a full cursor-less refetch and treat that snapshot as authoritative for
   removals.

### 11.7 Readiness: events and polling

While the app holds an events connection, act on `download` events (9.4) to move
rows out of `preparing`: start the transfer on `ready`, surface `failed` in the
downloads UI. Without an events connection, poll `GET /downloads` per 9.1.

### 11.8 Series monitoring

Call `POST /downloads/subscriptions/sync` on app open and from a periodic
WorkManager job. If the response has `registered > 0`, `GET /downloads` and
enqueue the newly registered rows. Enforce `delete_watched` and
`max_storage_bytes` locally; the server only soft-gates auto-registration (8.1).

---

## 12. Error code reference

Errors use a flat envelope:

```json
{ "error": "download_inactive", "message": "This download is no longer active" }
```

| HTTP | `error`                    | When                                                                      |
| ---- | -------------------------- | ------------------------------------------------------------------------- |
| 400  | `bad_request`              | Malformed body or missing required input.                                 |
| 400  | `device_id_required`       | Managed endpoint called without `X-Silo-Device-Id`.                       |
| 400  | `profile_required`         | Managed endpoint called without profile scope.                            |
| 400  | `invalid_status`           | Patch status is not `downloading` or `completed`.                         |
| 400  | `invalid_quality`          | Unknown public `quality`.                                                 |
| 400  | `invalid_format`           | Legacy/internal format value is invalid.                                  |
| 400  | `invalid_subtitle_ref`     | Subtitle ref is not `external:{i}` or `downloaded:{id}`.                  |
| 400  | `invalid_mode`             | Unknown subscription mode.                                                |
| 400  | `seasons_required`         | `specific_seasons` without `season_numbers`.                              |
| 400  | `invalid_season_numbers`   | A `season_numbers` value is outside `0–9999`.                             |
| 400  | `not_series`               | Subscription target is not a series.                                      |
| 401  | `unauthorized`             | Missing or invalid auth.                                                  |
| 403  | `feature_disabled`         | Downloads disabled on create paths.                                       |
| 403  | `forbidden`                | User not allowed to download.                                             |
| 403  | `transcode_disabled`       | Bitrate quality requested while transcode is disabled.                    |
| 404  | `no_downloadable_episodes` | Series/season download found no episode with a downloadable file.         |
| 404  | `not_found`                | Row/content/asset missing or outside profile access.                      |
| 409  | `download_inactive`        | Row is revoked or not servable.                                           |
| 429  | `download_limit_exceeded`  | Concurrent download cap hit.                                              |
| 429  | `download_quota_exceeded`  | Period quota hit.                                                         |
| 500  | `internal_error`           | Unexpected server error.                                                  |
| 501  | `quality_unavailable`      | Tone mapping is disabled, its selected mode is disallowed by policy, or enabled executor capabilities cannot support the requested quality. These configuration and policy failures use HTTP 501, distinct from capability-detection failures. |
| 501  | `bulk_quality_unavailable` | Series/season batch requested a non-original quality.                     |
| 503  | `capability_unavailable`   | Executor capability discovery failed temporarily; retry the same request. |
| 501  | `format_unavailable`       | Legacy/internal non-original direct download or missing prepare pipeline. |
| 503  | `capacity_unavailable`     | Compatible download-preparation executors are currently saturated.        |
| 503  | `unavailable`              | Downloads/offline assets/series monitoring service missing.               |

Access denials intentionally surface as `404` on manifest, artwork, subtitle, and
file endpoints so ids do not reveal out-of-scope content.

---

## 13. Out of scope for v1

Cross-device download visibility, DRM/leases, cumulative per-user storage quotas,
and server-initiated deletion of client files remain out of scope. Artifact garbage
collection may remove server-side prepared files only when no managed row still
references them.

---

## 14. Design notes (server internals)

Durable design decisions behind the contract above, kept here for server
maintainers. The implementation is `internal/downloads`.

### Storage model

Ephemeral web rows and managed device entries share one `downloads` table.
`device_id` is nullable: `NULL` means an ephemeral account-level row; a value
means a managed device-library entry, unique per
`(user, profile, device, content, episode)` via a partial unique index (movies
coalesce a `NULL` episode id so one movie is one entry per device). One table
and one endpoint family let web and mobile share the quality/format machinery.

### Prepared artifacts

Remux and transcode both need a finalized single file (`+faststart` requires a
finalization pass), so both go through a prepare-to-file job that writes a
`download_artifacts` row. Artifacts are deduplicated by
`(media_file_id, format, params_hash)` and shared across users and devices —
two devices requesting the same target reuse one encode. The artifact table is
a durable, leased job queue: transactional claims (`FOR UPDATE SKIP LOCKED`),
lease heartbeats, attempt counting, and a startup sweep guarantee a crash
mid-encode cannot strand a download in `preparing` or double-encode. Ready
artifacts are evicted LRU under a byte budget, but never while a managed row —
including a completed one representing a device's local library — still
references them.

### Progress sync ordering

Progress rows carry two server-owned facets, deliberately split:

- `event_at` — the client event time, clamped on ingest to
  `server_now + skew`, used only as the last-write-wins comparison key for the
  caller's own profile.
- `synced_seq` — a server-assigned monotonic marker set on every write, never
  client-influenced, and the sole basis for the `?since=` cursor.

A skewed or malicious clock can therefore at most claim "now" for its own
profile — authority it already has — and can never lock in a far-future win or
poison another device's cursor.

### Authorization

Household profiles share a `user_id`, so a user-only check would leak one
profile's downloads to another. Every managed endpoint authorizes the row on
`(user_id, profile_id, header device_id)` — `device_id` from the header only —
and byte/asset endpoints additionally re-check per-profile content and library
access before serving, so a stale or out-of-scope row cannot pull restricted
media by download id.

### Manifest stability

Manifests never carry presigned or expiring URLs. Artwork and subtitle
references are session-authenticated proxy paths rather than time-limited
tokens, so a manifest stored on-device stays valid indefinitely.

## Native API v2 registry migration

The native `/api/v2` registry uses the same download service and device identity.
`GET /api/v2/downloads` returns `items` and `page` with `has_more` and optional
`next_cursor`. The default limit is 50; `limit` accepts 1–100. Cursors bind
account, profile, policy and device. Entries sort by creation time and ID,
descending. Omitting `X-Silo-Device-Id` lists only account-level ephemeral rows;
supplying it lists only that profile and device's managed entries.

Registry IDs and media file IDs are strings. Entries retain their existing
quality, delivery format, revision and byte-count fields. Optional
`status_event_at` identifies the latest accepted client status event for the
current registry revision.

`PATCH /api/v2/downloads/{id}` requires the device header and:

```json
{
  "status": "completed",
  "updated_at": "2026-01-02T03:04:05.000Z",
  "revision": 1
}
```

Capture `updated_at` when local state changes, and retain both timestamp and
revision when retrying. The status is `downloading` or `completed`; future
timestamps are rejected. An older or equal event returns the current entry
without changing it. A different revision returns 409: reload the registry and
reconcile the new bytes instead of rewriting the old event's revision. Reports
cannot promote preparing, failed, or revoked entries into completion.
The request body is limited to 4 KiB.

Database migration initializes existing managed downloading/completed rows from
their last bridge update time. Subsequent v1 status writes advance this same
ordering fence using server time. Replacing an entry increments its revision and
clears its prior event timestamp, so a previous version's event cannot cross
the revision boundary.

`DELETE /api/v2/downloads/{id}` preserves managed deletion and ephemeral
cancellation behavior and returns a bodyless 204. Missing or incorrectly scoped
entries return 404.

`GET /api/v2/capabilities/downloads` exposes policy, quality presets and
`ordered_status`. The create, subscription, manifest and binary migrations are
separate from this registry checkpoint; clients must coordinate adoption of the
complete offline flow.

### Native file and asset delivery

The `file_delivery` capability advertises the native byte routes.
`GET` and `HEAD /api/v2/downloads/{id}/file` preserve attachment filenames,
MIME types, byte ranges (206, multipart ranges and 416), conditional requests
and bodyless HEAD metadata. Serving uses the existing per-user bandwidth and
rolling write-deadline behavior. Managed requests recheck current policy,
account/profile/device ownership, content access and the selected source or
prepared artifact's file restrictions.

`GET` and `HEAD /api/v2/downloads/{id}/file-proxy` additionally allow 307
redirects to the existing short-lived authorized proxy delivery URL when
`proxy_delivery` is true. Clients preserve the original method and Range
headers when following that URL. The ordinary file route remains local;
unavailable or ineligible proxy targets fall back to existing local delivery.

`GET /api/v2/downloads/{id}/artwork/{kind}` and
`GET /api/v2/downloads/{id}/subtitles/{ref}` require the device header.
Artwork kinds are poster, backdrop and logo; subtitle references retain the
existing external:index and downloaded:id identity. Current content access is
checked before asset delivery, and downloaded subtitle ownership must match the
entry's media file. These two asset routes preserve whole-object delivery and
private caching; they do not advertise byte ranges.

Failures before the body starts use v2 Problems. An upstream failure after bytes
have been written never appends JSON to the partial asset.

### Bounded native offline manifests

`bounded_manifests` advertises `GET /api/v2/downloads/{id}/manifest` and
`GET /api/v2/downloads/batches/{batch_id}/manifests`. Both require the current
profile and device identity and use the existing authorized manifest builder.

A native manifest has `manifest_version: 3`, a string `media_file_id`,
millisecond UTC `generated_at`, and authenticated `/api/v2/downloads/...`
artwork/subtitle references. It preserves the complete metadata, stable provider
identity, integrity, source or artifact media details, chapters, markers and
selected audio information. Bridge manifests remain version 2 with their
original v1 references.

Each encoded native manifest is limited to 1 MiB. A single manifest beyond that
bound returns 413; no fields or arrays are silently truncated. Batch responses
contain `items`, `skipped` and `page`. Their default `limit` is 3, maximum 10,
and applies to examined registry rows before metadata is built. This bounds a
page to ten complete manifests, with per-item `too_large` in `skipped` for an
oversized manifest. Existing revoked, not_found and error skip reasons remain.

Continue with `page.next_cursor` while `page.has_more` is true, even when
`items` is empty: skipped rows still advance the cursor. Cursors bind the
account, profile, access policy, device and batch. Source rows sort by creation
time and ID descending; this is live paging, so clients reconcile only after a
complete successful scan. Metadata shared by episodes is cached within each
page using the existing batch builder.

### Native subscription reads

`GET /api/v2/downloads/subscriptions` requires the active profile and
`X-Silo-Device-Id`. It returns `{items, page}` with a default limit of 50 and a
maximum of 100. The cursor binds the account, profile, access policy and device;
rows follow descending creation time and ID. Paused monitors remain visible.
Clients must finish every page before reconciling absent subscriptions.

`GET /api/v2/downloads/subscriptions/{id}` returns the same item shape with an
`ETag` response header. Each item includes its `etag`, original future cutoff
(`created_at`), latest-season anchor, explicit seasons (including season zero),
active flag, delete-watched preference and storage cap. Validators retain
persisted timestamp precision and device identity. Read responses are private
and require revalidation. A different account, profile or device cannot read a
monitor. The download capability exposes `subscription_reads` when these routes
are configured. The native mutation and bounded sync flow is described below.

### Native subscription changes and bounded sync

`POST /api/v2/downloads/subscriptions` accepts `series_id`, `mode`, optional
`season_numbers`, `delete_watched` and nonnegative `max_storage_bytes`.
It returns the persisted monitor with its validator. If the same device already
monitors that series, its current options and paused state are returned without
being changed. There is no durable creation receipt after deletion: do not
automatically replay an uncertain create; reconcile the monitor list first.

`PATCH /api/v2/downloads/subscriptions/{id}` and
`DELETE /api/v2/downloads/subscriptions/{id}` require `If-Match`. Patch accepts
mode, seasons, delete-watched, storage cap and active fields; absent fields remain
unchanged and explicit null is rejected. Edits preserve the future cutoff and
re-anchor latest-season selection only under the shared mode-change rules.
Delete stops monitoring and retains already-registered downloads.

After creating, resuming or widening a monitor, explicitly call
`POST /api/v2/downloads/subscriptions/sync` with
`{"subscription_id":"...","etag":"..."}` and the same device/profile headers.
The query `limit` defaults to 50 and is capped at 100 examined episodes.
Pass `page.next_cursor` as the next request's `cursor`, retaining the monitor
validator. The response contains `subscription_id`, `registered`, `examined` and
`page`. Continue when `page.has_more` is true even if `registered` is zero.
Iterate the bounded monitor list to sync every monitor. The traversal follows the
live catalog; subsequent refreshes pick up episodes inserted behind a cursor.

Sync checks series access and current options, prepares bounded metadata, then
rechecks the monitor under its transaction lock before registration. Registration
and storage accounting use that same transaction; a changed monitor returns 409
and requires a fresh monitor read before a new sync. Repeating a page skips
already-registered entries and may report zero new registrations. Paused monitors
never register episodes. The bridge retains immediate best-effort backfill on
create/edit and now discards delayed sync snapshots after a monitor change.
Capabilities `subscription_mutations` and `bounded_subscription_sync` identify
these operations separately from subscription reads.

### Native download creation

`POST /api/v2/downloads` returns 202 with `{items, skipped, page}` and an optional
`batch_id`. It uses the existing download policy, source selection, artifact
preparation, quota checks and device registration. The request requires
`content_id`; optional `episode_id`, string `media_file_id`, `quality` and the
shared playback `caps` payload select the source and delivery target. All six
quality choices remain supported, including `1mbps`. Detailed decoder evidence
uses the existing playback validator. Explicit file/episode selection must match
the requested catalog identity.

With a device header, a single-item request requires `expected_revision`: zero
requires an absent managed entry; a positive value together with `expected_download_id` identifies the exact entry
being reused, revived or replaced. A stale revision returns 409 before replacing newer
bytes. A losing database compare-and-set cannot acknowledge a different winner as
the requested target. Without a device header, creation retains the account's
ephemeral transfer flow and rejects managed revision guards.

For a series or season, send `series:true`, a client-selected `batch_id`, and
optional `season_number` (zero selects Specials). The `limit` query defaults to
50 and is capped at 100 examined episodes. Keep batch, content, season, quality
and device/profile identity unchanged while following `page.next_cursor` through
the `cursor` query. Continue through empty `items` when `page.has_more` is true;
`skipped` explicitly reports episodes with no file. Bulk quality remains original
only, matching the existing supported flow.

Batch pages preserve existing managed entries by default, including their chosen
bytes, completion/terminal status, revision and previous batch membership. An
optional `expected_entries` object supplies at most 100 episode-ID to
`{id, revision}` guards for replacements or revivals on that page. A zero revision
requires absence and omits `id`. Positive revisions require the exact registry ID
so a deleted-and-recreated entry cannot inherit old replacement authority. Returned entries
carry their actual batch identity; fetch an individual manifest for a reused
entry that belongs to an earlier batch. Newly registered pages share the
requested batch ID. The traversal reads the live catalog, so refreshes may be
needed for episodes inserted behind a cursor.

Creation has no durable response receipt and does not promise atomic application
of all replacements in a page. Do not automatically replay an uncertain request,
refresh revision guards, or fall back to v1. Reconcile the registry after
uncertainty, then make an explicit new request for unresolved work. This also
prevents duplicate ephemeral transfers. The download capability exposes
`bounded_creation` when this full native creation flow is configured.

## Native v2 direct original delivery

GET and HEAD `/api/v2/direct-download?file_id={id}` preserve synchronous original-file delivery. GET and HEAD `/api/v2/direct-download-proxy?file_id={id}` preserve the proxy-aware variant. `file_id` is a canonical positive decimal string; `format` may be absent, empty or `original`. Duplicate and unknown query parameters return 422. These routes use the existing download capability/policy service; they do not create a managed download, artifact or playback session.

Every request applies account authentication, viewer/demo gates, account download policy and catalog/file access. Header callers may supply the existing profile and PIN headers. Browser navigation retains the existing account `token` query fallback: the selected profile/PIN does not travel in that URL, and the request uses account-scoped access without a selected profile. This migration introduces no new signed browser grant or profile query credential. Account URLs remain secrets with the limitations described in section 4.10.

The service opens the authorized source file and closes it after streaming. Success preserves Content-Disposition, original MIME type, Content-Length, Last-Modified, HEAD, ranges/206 and conditional/304 semantics. Missing files return 404. Malformed input returns 422; invalid range 416 retains Content-Range. Failures before output become redacted v2 problems. A failure after output has begun aborts the stream instead of appending JSON; neither partial bytes nor a lost response prove completion. No replay or durable local-file receipt is provided.

The proxy producer resolves permission before creating a token for the selected FileTarget. The existing planner, short-lived signed target, preflight and local fallback remain authoritative; callers cannot supply a path or proxy URL. A successful proxy preflight may yield 307. HEAD releases its provisional planner reservation; GET retains the existing reservation lifecycle, with no new completion/cleanup guarantee. Preflight failure releases the reservation and falls back to a freshly authorized local serve. A redirect is not proof of delivery, and an issued proxy token retains its existing expiration/revocation limits.

The bundled DownloadVersionPicker captures account/session and current profile/PIN UI authority, probes once with HEAD, and launches browser GET using the identical captured account URL only while that authority and selection remain current. Closing/replacing the picker or changing authority prevents a late launch. There is no refresh replay, whole-file buffering or proxy URL fabrication. HEAD success and navigation dispatch do not prove that the subsequent browser download completed. No first-party direct-proxy producer was found; exact native method-family inventories remain required for ordinary ratification. Managed downloads, worker routes and Jellyfin retain their separate implementations.
