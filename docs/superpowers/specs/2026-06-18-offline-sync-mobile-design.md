# Offline sync for mobile (downloads v2) — design

**Goal:** Let mobile clients (Android, Apple) download media for **fully offline** playback and keep
watch state consistent across reconnects. Three pillars: (1) a **device-scoped download registry** —
the server tracks what each device has downloaded; (2) an **offline playback manifest** — one bundle
giving the client everything it needs to play with no network (metadata, subtitle files, artwork,
chapter + intro/credits markers); (3) **offline progress reconciliation** — the client queues watch
progress while offline and the server merges it on reconnect (last-write-wins). Downloads are requested
through an admin-gated **quality** ladder (`original`, then bitrate presets), with server-side
**transcode-to-a-single-file** for bitrate requests and compatibility fallback for devices that cannot
play the source directly.

**Status:** Approved — implementing. This capability cleared the Silo v1 scope gate on
2026-06-18 (via the **v1 capability proposal** triage; template
`.github/ISSUE_TEMPLATE/v1-capability-proposal.yml`), and the offline-sync/downloads-v2 work is now
being implemented phase by phase against this design. Per the decision below, the
`/api/v1/downloads` contract is **reshaped, not just extended** — acceptable because the change lands
**before** scope lock (`docs/architecture/v1-scope.md`) and **before** any mobile client ships against
it. This document began as the design input to that proposal and now serves as the implementation
reference; it is kept in sync as the implementation lands.

Decisions locked with the requester:
- **Replace** the existing `internal/download` package with a new unified package (`internal/downloads`)
  that absorbs today's download behavior and adds the three pillars.
- **The existing `/api/v1/downloads` contract may be reshaped, not just extended.** The only current
  consumer is the first-party web app, which is updated in lockstep. So downloads v2 is a single
  coherent surface: one device-aware `downloads` table and one `/downloads/*` endpoint family — *not* a
  frozen legacy table plus a parallel registry. (This intentionally diverges from the v1 additive-only
  API rule, which is acceptable here because the change lands **before** scope lock and **before** any
  mobile client ships against it — exactly the right time to reshape. The proposal must call this out so
  reviewers/clients know the download contract changed pre-lock.)
- **Quality is user-facing; delivery format is internal.** Clients request `quality` from the ordered
  ladder `original > 20mbps > 10mbps > 5mbps > 2mbps > 1mbps`. `remux` is not a preset; it is a
  server-selected `delivery_format` when `original` needs container/audio compatibility. Bitrate
  presets are gated by a server setting **and** the per-user `download_transcode_allowed` flag.
- **Transcode-to-single-file is in v1.** (Today's transcode pipeline only emits ephemeral HLS segments.)
- **No expiry, no lease, no DRM.** Downloaded files persist on-device until the user deletes them; the
  server can only revoke *future* downloads via `users.download_allowed`.
- **No cross-device visibility** in v1 (a device sees its own entries, not other devices').

**Implementation update (2026-06-20):** the current client-facing contract is documented in
`docs/downloads-api.md`. The public request field is `quality`, capabilities expose
`quality_presets`, rows expose `quality`, `effective_quality`, `delivery_format`,
`target_bitrate_kbps`, and `revision`, and batch manifests are implemented at
`GET /api/v1/downloads/batches/{batch_id}/manifests`.

> Commands and paths in this document are repository-relative; assume the repository root is the cwd.

## Why this shape

- **One device-aware table, not two.** The original plan kept today's `downloads` table
  (`migrations/sql/042_downloads.sql`, keyed on `user_id`) frozen and added a separate device registry,
  purely to avoid breaking the existing contract. With that constraint lifted, the cleaner model is a
  single `downloads` table reshaped to carry `device_id` (and `profile_id`). `device_id` is **nullable**:
  - `device_id IS NULL` → an **ephemeral / account-level** download (today's web-app browser download:
    pick a version, pull the file; the row is a convenience record, not a managed library entry).
  - `device_id` set → a **managed device-library entry** (a phone "has" this file; durable; unique per
    `(user, profile, device, content, episode)`; the target of manifest + progress reconciliation).
  One table, one endpoint family, and the format/transcode machinery is shared by web and mobile alike.
- **Device identity already exists in Postgres.** `migrations/sql/180_user_devices.sql` defines
  `public.user_devices` keyed `(user_id, profile_id, device_id)` with FKs to `users` and
  `user_profiles`. Device id arrives in the `X-Silo-Device-Id` header (see
  `internal/api/handlers/settings.go:37`). Managed entries reference it; ephemeral rows leave it NULL.
- **The last-write-wins primitive already exists.** `userstore.UserStore.SetProgressIfNewer(... updatedAt) (bool, error)`
  (`internal/userstore/store.go:26`, implemented in `internal/userdb/progress.go:131`) is exactly the
  merge semantics offline reconciliation needs. Progress reconciliation is therefore **additive changes
  to the existing progress endpoints**, not new machinery, and it deliberately stays in the watch-state
  domain rather than being folded into the downloads package.
- **Manifests must never carry presigned URLs.** `catalog.ItemDetail` (`internal/catalog/detail.go`)
  exposes `PosterURL`/`BackdropURL` as short-lived presigned S3 URLs — useless for an offline bundle
  that may be opened days later. The manifest carries **stable** references (thumbhashes inline +
  authenticated proxy endpoints the client fetches once at download time), so a stored manifest never
  expires.
- **Remux and transcode both need a produced file, not a live pipe.** A clean, seekable, resumable
  download requires a finalized file (`-movflags +faststart` relocates the moov atom in a finalization
  pass — impossible over a pure pipe). `internal/playback`'s current `ServeRemux`/`StartTranscode`
  stream ephemeral output. So remux and transcode share one **prepare-to-file** job pipeline; only
  `original` is served straight from the source on disk.

## The replacement: `internal/downloads` (new package)

`internal/download` (singular) is replaced by `internal/downloads` (plural). The new package owns the
whole downloads domain; everything below lives in it unless noted.

```
internal/downloads/
  model.go         — Download (web + device, one type), Artifact, OfflineManifest,
                     quality/delivery constants, status constants, sentinel errors
  repo.go          — Postgres CRUD: downloads (reshaped), download_artifacts
  service.go       — orchestration: permission + quota checks, quality policy, create/list/serve
  policy.go        — DownloadQualityResolver (quality ladder + delivery decision + server/user gates)
  bandwidth.go     — BandwidthManager (ported from internal/download/bandwidth.go)
  limiter.go       — QuantityLimiter (ported from internal/download/limiter.go)
  manifest.go      — ManifestBuilder (assembles OfflineManifest; strips all presigned URLs)
  artifacts.go     — ArtifactManager (async prepare-to-file jobs: remux + transcode, dedup, cleanup)
  serve.go         — file serving for original source and prepared artifacts
```

**What it absorbs / reshapes** from `internal/download`: the `downloads` table and its endpoints, now
reshaped to be device-aware and quality/delivery-aware; `BandwidthManager`; `QuantityLimiter`; the `Service`
permission/quota/file-serving logic; and the `config.DownloadConfig` integration.

**What stays out of the package (by design):**
- **Progress reconciliation** is watch-state, not downloads. It is additive changes to
  `internal/api/handlers/progress.go` + `internal/userstore`/`internal/userdb`, reusing
  `SetProgressIfNewer`. Folding it into `internal/downloads` would couple two domains and duplicate the
  LWW write.
- **The encode/remux primitive** stays in `internal/playback` (it owns ffmpeg). `internal/downloads`
  calls a new `playback.PrepareFile(...)`; it does not shell out to ffmpeg itself.

**Cutover strategy:** because the web app is the only consumer and is updated in lockstep, Phase 0 is a
*reshape*, not a verbatim port: move the logic into `internal/downloads`, reshape the table + endpoints
(add device-awareness plus quality/delivery fields), update the web app's download hooks/components, delete
`internal/download`. The handler `internal/api/handlers/downloads.go` already depends only on a narrow
`DownloadService` interface, so the swap is mechanical; its request/response DTOs gain the new fields.

## Data model (Goose migrations)

Migrations are timestamped, created with `make migrate-create NAME=...` (never hand-numbered; never
`goose fix`).

### 1. Reshape `downloads` (device-aware, format-aware)

A single migration alters the existing table. Downloads ship default-off with little/no production data,
so widening it in place is safe.

```sql
-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.downloads
    ADD COLUMN profile_id  text,                         -- NULL for ephemeral/web rows
    ADD COLUMN device_id   text,                         -- NULL = ephemeral; set = managed device entry
    ADD COLUMN format      text NOT NULL DEFAULT 'original',
    ADD COLUMN artifact_id text;                         -- set for remux/transcode once prepared

ALTER TABLE public.downloads
    ADD CONSTRAINT downloads_format_check CHECK (format IN ('original','remux','transcode'));

-- Widen the status enum to cover the managed-entry lifecycle alongside the existing serve states.
ALTER TABLE public.downloads DROP CONSTRAINT downloads_status_check;
ALTER TABLE public.downloads ADD  CONSTRAINT downloads_status_check
    CHECK (status IN ('queued','downloading','completed','failed','cancelled',  -- existing
                      'registered','preparing','ready','revoked'));             -- managed entries

-- One managed entry per (user, profile, device, content, episode). Ephemeral rows (NULL device_id)
-- are exempt via the partial index.
CREATE UNIQUE INDEX downloads_device_entry_uidx
    ON public.downloads (user_id, profile_id, device_id, content_id, COALESCE(episode_id, ''))
    WHERE device_id IS NOT NULL;

CREATE INDEX downloads_device_idx   ON public.downloads (user_id, profile_id, device_id) WHERE device_id IS NOT NULL;
CREATE INDEX downloads_artifact_idx ON public.downloads (artifact_id) WHERE artifact_id IS NOT NULL;

-- Composite FK to user_devices. MATCH SIMPLE (the default) skips the check whenever any column is NULL,
-- so ephemeral rows (NULL device_id) are not constrained while managed entries are.
ALTER TABLE public.downloads
    ADD CONSTRAINT downloads_device_fkey
    FOREIGN KEY (user_id, profile_id, device_id)
    REFERENCES public.user_devices(user_id, profile_id, device_id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.downloads DROP CONSTRAINT IF EXISTS downloads_device_fkey;
DROP INDEX IF EXISTS downloads_artifact_idx;
DROP INDEX IF EXISTS downloads_device_idx;
DROP INDEX IF EXISTS downloads_device_entry_uidx;
ALTER TABLE public.downloads DROP CONSTRAINT IF EXISTS downloads_format_check;
ALTER TABLE public.downloads DROP CONSTRAINT IF EXISTS downloads_status_check;
ALTER TABLE public.downloads ADD  CONSTRAINT downloads_status_check
    CHECK (status IN ('queued','downloading','completed','failed','cancelled'));
ALTER TABLE public.downloads
    DROP COLUMN artifact_id, DROP COLUMN format, DROP COLUMN device_id, DROP COLUMN profile_id;
-- +goose StatementEnd
```

Status lifecycle:
- **ephemeral / web** (`device_id IS NULL`): `queued → downloading → completed` / `failed` /
  `cancelled` — exactly today's behavior, now also able to request `remux`/`transcode`.
- **managed entry** (`device_id` set):
  - `original`: `registered → ready` immediately (client pulls the source), then client reports
    `completed`.
  - `remux` / `transcode`: `registered → preparing` (artifact job runs) `→ ready → completed`; or
    `failed`.
  - `revoked`: admin/permission revocation of *future* access. Cannot delete the on-device file (no DRM
    by decision); it only stops the server serving new bytes.

The unique index uses `COALESCE(episode_id,'')` so movies (NULL `episode_id`) collapse to one managed
entry per `(device, content)` without NULL-comparison surprises.

### 2. `download_artifacts` — prepared (remux/transcode) files, deduplicated

```sql
-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.download_artifacts (
    id                text        NOT NULL,
    media_file_id     integer     NOT NULL REFERENCES public.media_files(id) ON DELETE CASCADE,
    format            text        NOT NULL,            -- 'remux' | 'transcode'
    params_hash       text        NOT NULL,            -- sha256 of the encode parameters (see below)
    container         text        NOT NULL DEFAULT 'mp4',
    codec_video       text        NOT NULL DEFAULT '',
    codec_audio       text        NOT NULL DEFAULT '',
    resolution        text        NOT NULL DEFAULT '',
    audio_track_index integer     NOT NULL DEFAULT -1,
    output_path       text        NOT NULL DEFAULT '', -- absolute path on the server's artifact volume
    file_size         bigint      NOT NULL DEFAULT 0,
    status            text        NOT NULL DEFAULT 'queued',
    error_message     text        NOT NULL DEFAULT '',
    -- Durable-queue / crash-recovery columns (see "Durable artifact queue" below).
    attempts          integer     NOT NULL DEFAULT 0,
    max_attempts      integer     NOT NULL DEFAULT 3,
    lease_owner       text,                              -- worker/node id holding the running lease
    lease_expires_at  timestamptz,                       -- NULL unless status='running'
    next_retry_at     timestamptz,                       -- backoff gate for re-enqueue after a failure
    created_at        timestamptz NOT NULL DEFAULT now(),
    completed_at      timestamptz,
    last_used_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT download_artifacts_pkey PRIMARY KEY (id),
    CONSTRAINT download_artifacts_unique UNIQUE (media_file_id, format, params_hash),
    CONSTRAINT download_artifacts_status_check CHECK (status IN ('queued','running','ready','failed'))
);

CREATE INDEX download_artifacts_lru_idx       ON public.download_artifacts (last_used_at) WHERE status = 'ready';
-- Claimable work: queued rows whose backoff has elapsed, plus running rows whose lease has expired.
CREATE INDEX download_artifacts_claimable_idx ON public.download_artifacts (status, next_retry_at);
CREATE INDEX download_artifacts_lease_idx     ON public.download_artifacts (lease_expires_at) WHERE status = 'running';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.download_artifacts;
-- +goose StatementEnd
```

`params_hash = sha256(format | container | codec_video | codec_audio | resolution | audio_track_index | subtitle_burn_in)`.
Two devices (or a web user and a phone) requesting the same 1080p H.264/AAC of the same source share one
artifact. `last_used_at` drives LRU cleanup; `output_path` is on a configured artifact volume. The
`attempts`/`lease_*`/`next_retry_at` columns make the table a **durable, recoverable job queue** (see the
"Durable artifact queue" subsection) so a crash mid-encode cannot strand a download in `preparing`.

### 3. Server settings (no DDL)

`server_settings` is key/value, so new keys need no migration — written when an admin saves them, parsed
in `internal/config/db_loader.go`:
- `download.transcode_enabled` (bool, default **false**) — server gate for transcode-to-file.
- `download.artifact_dir` (string) — artifact output volume; defaults under the existing transcode/temp root.
- `download.max_concurrent_prepares` (int, default 2) — encode/remux worker-pool size.
- `download.artifact_max_bytes` (int64, default 0 = unlimited) — LRU eviction budget for artifacts.

`config.DownloadConfig` (`internal/config/config.go:299`) gains the matching fields; they hot-reload via
the existing 30s-TTL cache (`internal/download/service.go:107` → ported), and the keys are registered as
non-restart in `internal/config/restart_keys.go`.

## Quality policy

Resolved in `internal/downloads/policy.go` at create time, for both web and device flows:

```go
const (
    QualityOriginal = "original"
    Quality20Mbps   = "20mbps"
    Quality10Mbps   = "10mbps"
    Quality5Mbps    = "5mbps"
    Quality2Mbps    = "2mbps"
    Quality1Mbps    = "1mbps"
)

type QualityDecision struct {
    RequestedQuality  string
    EffectiveQuality  string
    DeliveryFormat    string // original | remux | transcode
    TargetBitrateKbps int
    RequiresArtifact  bool
}

func (DownloadQualityResolver) Resolve(requested string, user *models.User,
    cfg config.DownloadConfig, file *models.MediaFile, caps playback.ClientCapabilities,
    artifactsAvailable bool) (QualityDecision, error)
```

Decision tree:
1. Empty quality defaults to `original`.
2. `original` direct-plays the source unless supplied device caps require a compatibility artifact.
   Container/audio-only compatibility records `delivery_format=remux`; video transcode fallback records
   `effective_quality=20mbps`, `delivery_format=transcode`.
3. Bitrate presets require `cfg.TranscodeEnabled` (server), `user.DownloadTranscodeAllowed`, and an
   available artifact pipeline. Server gate off → `ErrTranscodeDisabled` (403). User flag off →
   `ErrDownloadNotAllowed` (403). Missing artifact pipeline → `ErrQualityUnavailable` (501).
4. `remux` and `transcode` as request values are invalid public qualities.

Target codec/resolution for compatibility artifacts and bitrate transcodes is chosen by reusing `playback.Resolve` /
`playback.SelectVersion` (`internal/playback/resolver.go:60`) against the device's declared capabilities
and the admin transcode ceilings, so download encoding matches streaming decisions exactly (no
duplicated codec logic).

## Transcode-to-file pipeline (v1)

`internal/downloads/artifacts.go` — `ArtifactManager`. Remux and transcode share this; `original` never
enters it.

**On create of a remux/transcode download (web or device):**
1. Resolve target params (codec/res/container/audio track) → compute `params_hash`.
2. `INSERT ... ON CONFLICT (media_file_id, format, params_hash) DO NOTHING` into `download_artifacts`;
   read the row. If `status = ready`, link `downloads.artifact_id`, set `status = ready`,
   `file_size = artifact.file_size`, bump `last_used_at`. Done — no new encode.
3. Otherwise the download row is `preparing`; if the artifact is freshly `queued`, enqueue an encode job.

**Encode worker** (bounded pool of `download.max_concurrent_prepares`, hosted on the existing
`internal/taskmanager` worker machinery so jobs are tracked, observable in admin, and cancellable):
1. **Claim a job transactionally** (see "Durable artifact queue" below) — never just "enqueue freshly
   queued rows," which loses work on restart.
2. Call a new `playback.PrepareFile(ctx, opts)` that builds **single-file** ffmpeg args and writes to
   `output_path + ".part"`, then atomically renames to `output_path`:
   - **remux:** `-map ... -c copy -movflags +faststart -f mp4` (fast, near disk-throughput).
   - **transcode:** the existing encode args (reuse the `buildFFmpegArgs` logic / `TranscodeOpts` at
     `internal/playback/transcode.go:30`, incl. HW-accel, audio-track selection, optional subtitle
     burn-in) but targeting `-movflags +faststart -f mp4` to a file instead of `-f hls`.
   `TranscodeOpts` already carries every needed field (`InputPath`, `TargetCodecVideo/Audio`,
   `TargetResolution`, `HWAccel`, `AudioTrackIndex`, `SubtitleBurnIn`, `TotalDuration`).
3. On success: artifact → `ready` (`file_size`/`completed_at`, clear lease); flip every linked download
   row → `ready`; publish a `user_state`/`downloads` event on the events hub (`internal/events`) so a
   waiting client pulls without polling.
4. On failure: increment `attempts`, set `error_message`/`next_retry_at` (backoff). If
   `attempts >= max_attempts` → artifact `failed` and linked rows `failed`; otherwise return it to
   `queued` for retry. Either way, delete the stale `.part` file.

**Durable artifact queue (crash/restart recovery).** The artifact table is the queue of record, so a
process exit mid-encode cannot strand a download in `preparing` forever:
- **Transactional claim:** a worker claims a job with a single statement —
  `UPDATE download_artifacts SET status='running', lease_owner=$me, lease_expires_at=now()+$lease, attempts=attempts+1
   WHERE id = (SELECT id FROM download_artifacts
               WHERE (status='queued' AND (next_retry_at IS NULL OR next_retry_at <= now()))
                  OR (status='running' AND lease_expires_at < now())   -- steal expired leases
               ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING *`.
  `FOR UPDATE SKIP LOCKED` makes concurrent workers (and multiple nodes) safe without double-encoding.
- **Lease heartbeat:** a running worker periodically extends `lease_expires_at`; if it dies, the lease
  expires and another worker (or the startup sweep) reclaims the job.
- **Startup sweep:** on boot, reset `running` rows with an expired lease back to `queued` (bounded by
  `max_attempts`), and re-link any `preparing` download rows to their artifact's real state. A `ready`
  artifact whose `output_path` is missing on disk is reset to `queued` (the file was lost).
- **Idempotency:** the encode writes to `output_path + ".part"` and atomically renames; a reclaimed job
  overwrites its own `.part`. `output_path` is deterministic from `(media_file_id, format, params_hash)`.
- **No silent stranding:** a download stuck in `preparing` past a bound is surfaced (admin task + a
  `failed` transition with reason), never left invisibly hung.

**Serving** (`internal/downloads/serve.go`, `GET /downloads/{id}/file`):
- `original`: open `media_files.file_path`, `http.ServeContent` (Range/resume) wrapped in the ported
  `BandwidthManager.ThrottledReader`. Same path as today's `serveFileDownload`.
- `remux`/`transcode`: require `status = ready`; serve `artifact.output_path` the same way (Range/resume
  + throttle); bump `last_used_at`.

**Cleanup:** an admin/cron job (mirroring `internal/playback/transcode_cleanup.go`) evicts `ready`
artifacts whose `last_used_at` is older than a retention window or when the total exceeds
`download.artifact_max_bytes` (LRU), but never one still linked by an active managed download row,
including completed rows that represent a device's local library state.
Whatever is dropped is logged (no silent truncation).

## Offline playback manifest

`internal/downloads/manifest.go` builds `OfflineManifest` from `catalog`'s existing detail path (reusing
`catalog.ItemDetail` assembly — markers, chapters, subtitle list, versions) and then **strips every
presigned URL**. The manifest is stable: a client can store it and re-read it offline indefinitely.
Manifest + asset endpoints are only meaningful for managed device entries (`device_id` set).

```go
type OfflineManifest struct {
    DownloadID        string `json:"download_id"`
    ContentID         string `json:"content_id"`
    EpisodeID         string `json:"episode_id,omitempty"`
    Type              string `json:"type"` // movie | episode
    Revision          int    `json:"revision"`
    Quality           string `json:"quality"`
    EffectiveQuality  string `json:"effective_quality"`
    DeliveryFormat    string `json:"delivery_format"`
    TargetBitrateKbps int    `json:"target_bitrate_kbps"`
    MediaFileID       int    `json:"media_file_id"`
    FileSize          int64  `json:"file_size"`

    Title         string   `json:"title"`
    Year          int      `json:"year,omitempty"`
    Overview      string   `json:"overview,omitempty"`
    Runtime       int      `json:"runtime,omitempty"`
    ContentRating string   `json:"content_rating,omitempty"`
    Genres        []string `json:"genres,omitempty"`
    SeriesID      string   `json:"series_id,omitempty"`
    SeriesTitle   string   `json:"series_title,omitempty"`
    SeasonNumber  *int     `json:"season_number,omitempty"`
    EpisodeNumber *int     `json:"episode_number,omitempty"`

    // Artwork: stable thumbhashes inline (immediate offline placeholder) + authenticated proxy URLs
    // the client downloads ONCE at download time. Never presigned S3 URLs.
    PosterThumbhash   string `json:"poster_thumbhash,omitempty"`
    BackdropThumbhash string `json:"backdrop_thumbhash,omitempty"`
    ArtworkURLs       struct {
        Poster   string `json:"poster,omitempty"`
        Backdrop string `json:"backdrop,omitempty"`
        Logo     string `json:"logo,omitempty"`
    } `json:"artwork_urls"`

    // Playback metadata
    Container  string `json:"container"`
    CodecVideo string `json:"codec_video"`
    CodecAudio string `json:"codec_audio"`
    Resolution string `json:"resolution"`
    HDR        bool   `json:"hdr"`
    Duration   int    `json:"duration_seconds"`

    Chapters []OfflineChapter `json:"chapters,omitempty"`
    Intro    *Marker          `json:"intro,omitempty"`
    Credits  *Marker          `json:"credits,omitempty"`
    Recap    *Marker          `json:"recap,omitempty"`
    Preview  *Marker          `json:"preview,omitempty"`

    Subtitles []OfflineSubtitle `json:"subtitles"`

    // Stable identity so the client can re-resolve content_id after a server-side rescan
    // (mirrors userstore.WatchIdentity at internal/userstore/types.go:130).
    StableIdentity OfflineIdentity `json:"stable_identity"`

    ManifestVersion int    `json:"manifest_version"`  // bump on DTO shape change
    GeneratedAt     string `json:"generated_at"`      // RFC3339
}

type OfflineSubtitle struct {
    Language        string `json:"language"`
    Format          string `json:"format"`   // srt | ass | vtt
    Forced          bool   `json:"forced"`
    HearingImpaired bool   `json:"hearing_impaired"`
    External        bool   `json:"external"`
    FetchURL        string `json:"fetch_url"` // authenticated proxy; client downloads at download time
    FileSize        int64  `json:"file_size,omitempty"`
}
```

**Artwork/subtitle proxies** (replace presigning):
- `GET /downloads/{id}/artwork/{kind}` (`kind` ∈ `poster|backdrop|logo`) reads the raw stored path and
  **streams the bytes** through the existing image resolver instead of redirecting to a presigned URL.
  The client caches these once.
- `GET /downloads/{id}/subtitles/{ref}` (`ref` encodes `external:{index}` or `downloaded:{id}`) streams
  the subtitle file (external sub from disk; downloaded sub from S3 via the existing subtitle manager).

Both are authorized under the **full managed-entry rule** (see "Security & reliability invariants"):
the row must match `(user_id, profile_id, header device_id)`, and content/library access is **re-checked
for the requesting profile** before any bytes are served (a download id alone never authorizes access).
They are session-authenticated rather than time-limited tokens, so a manifest stored on-device stays valid.

## Progress reconciliation

Two **additive** changes. The naïve version (route the client's wall-clock straight into
`SetProgressIfNewer` and drive `?since=` off the same column) is **unsafe**: a skewed or malicious device
can submit a far-future timestamp, win LWW permanently over genuinely-newer server activity, and poison
the cursor (a future-dated row makes a reader advance its cursor past real writes). So the design
**separates client event time from server ordering**.

The watch-progress row gains two server-owned facets (LWW key vs. cursor ordering):
- **`event_at`** — the client-observed wall-clock for the progress event, used as the **LWW comparison
  key**, but **bounded on ingest**: `event_at = min(client_updated_at, server_now + skew)` with a small
  skew window (e.g. 2m); a value past the window is clamped to `server_now` (and logged). For an online
  write `event_at = server_now`. Bounding means a client can at most claim "now" for its **own profile's**
  progress — which it is already entitled to do — and can never lock in a far-future value.
- **`synced_seq`** — a **server-assigned monotonic** marker set on every write (a sequence, or
  server-ingest timestamp with a tie-breaker). It is **never** client-influenced and is the sole basis
  for the `?since=` cursor.

**Write — `POST /api/v1/sync/progress`** (`internal/api/handlers/progress.go:230`): add an optional
`updated_at` (RFC3339) per item = the client event time. The handler clamps it (above) and merges via an
extended `SetProgressIfNewer` whose comparison is on the **bounded `event_at`**, while the server stamps
`synced_seq` itself. Absent `updated_at` → behavior is exactly as today (server `now()`), so existing
callers (incl. watchsync) are unaffected. Completion is never inferred from the timestamp alone — the
existing watched-threshold logic still gates `completed`.

```go
type syncProgressItem struct {
    MediaItemID    string  `json:"media_item_id"`
    Position       float64 `json:"position"`
    Duration       float64 `json:"duration"`
    ForceOverwrite bool    `json:"force_overwrite"`
    UpdatedAt      *string `json:"updated_at,omitempty"` // NEW: RFC3339; client EVENT time (server clamps it)
}
```

**Read — `GET /api/v1/progress?since=<cursor>`** (`internal/api/handlers/progress.go:86`): the `since`
cursor is an **opaque server token over `synced_seq`**, not a client timestamp. When present, only rows
with `synced_seq > cursor` are returned (use a sequence, or `>=` + id de-dup if a timestamp marker is
used, to avoid tie loss). The response carries the next cursor. This makes delta delivery immune to
client clock skew. Requires `UserStore.ListProgressSince(ctx, profileID, cursor)` in both backends
(`internal/userdb`, `internal/userstore/pgstore`) ordering/filtering on `synced_seq`. Absent `since` →
identical to today.

Adding a `UserStore` method (and the `event_at`/`synced_seq` columns to the per-user `watch_progress`
schema) touches every stub implementation; ship the implementations, the schema change, and the shared
store test suite update (`internal/userstore/storetest`) in the same PR.

**Net safety:** honest clients still get correct "most-recent actual watch wins" LWW; a dishonest clock
is bounded to claiming "now" for its own profile (no authority it didn't already have), and cursors —
the cross-device delivery mechanism — never depend on a client clock at all.

## API surface (one `/downloads/*` family; existing endpoints reshaped)

The web app is updated in lockstep, so existing endpoints are reshaped rather than frozen.

**Reshaped (existing):**
```
POST   /downloads            body {content_id, episode_id?, file_id?, quality?, series?, caps?};
                             X-Silo-Device-Id present → managed entry; absent → ephemeral/web row.
                             `quality` is original by default; bitrate presets are single-item only.
GET    /downloads            managed entries for the CALLING device (device from header); ephemeral rows otherwise
DELETE /downloads/{id}       remove a row owned by (user, profile, header device)
GET    /downloads/{id}/file  serve original (source) or prepared artifact (Range/resume + throttle)
GET|HEAD /direct-download    one-shot browser download, original-only (browser-friendly, token-in-URL)
```
The response DTO gains `device_id`, `quality`, `effective_quality`, `delivery_format`,
`target_bitrate_kbps`, `revision`, artifact-derived readiness, and the new statuses.
**`device_id` is authoritative only from the `X-Silo-Device-Id` header — never from the body or query**
(a body/query value could only ever be a display hint, and is not accepted as authority).

**Bitrate qualities are single-item only.** A series batch (`series:true`) and
`/direct-download` are **original-quality only**: `/direct-download` streams
synchronously and cannot wait on an async encode, and a series batch would otherwise
fan out N encode jobs. A client that wants prepared transcodes for a series requests
each episode individually. `DownloadQualityResolver` rejects bulk bitrate requests
with `ErrBulkQualityUnavailable` / HTTP 501.

**New — capability / feature detection** (pattern from `internal/api/handlers/notifications.go:324`):
```
GET /downloads/capability
→ { "enabled": bool, "download_allowed": bool,
    "quality_presets": ["original","20mbps","10mbps","5mbps","2mbps","1mbps"],
    "transcode_enabled": bool, "transcode_user_allowed": bool,
    "season_download": bool, "series_monitoring": bool }
```

**New — managed-entry lifecycle + offline bundle:**
```
PATCH /downloads/{id}                         body {status:"downloading"|"completed"}
GET   /downloads/{id}/manifest                → OfflineManifest
GET   /downloads/batches/{batch_id}/manifests → {manifests:[OfflineManifest]}
GET   /downloads/{id}/artwork/{kind}          → image bytes  (poster|backdrop|logo)
GET   /downloads/{id}/subtitles/{ref}         → subtitle bytes
```

**Progress reconciliation:** additive fields on existing `POST /sync/progress` and `GET /progress`
(above). No new routes.

Routes mount in the existing authenticated group where the current download routes live
(`internal/api/router.go:2121`). **Every managed-entry endpoint — create, list, `PATCH`, `DELETE`,
`/file`, `/manifest`, `/artwork`, `/subtitles` — is profile-scoped (`RequireProfile`) and authorizes the
row on `(user_id, profile_id, header device_id)`** (not `user_id` alone — household profiles share a
`user_id`, so a user-only check would leak one profile's downloads to another). A managed call requires
`X-Silo-Device-Id` (missing header on a managed create → `400 device_id_required`); ephemeral web rows
omit it and stay account-scoped. Byte-serving and asset endpoints additionally **re-check per-profile
content/library access** (the existing access filter / `EnsureAccessible`) before serving, so a stale or
out-of-scope row — e.g. a child profile, or a profile that lost library access — cannot pull restricted
media by download id even if the row exists.

## Data flows

**Ephemeral web download (reshaped existing flow, quality-aware)**
```
POST /downloads {content_id, quality:"original", caps:{...}}            (no device id → ephemeral)
 → Resolve quality/delivery · permission · quota
 → compatibility artifact: ArtifactManager.Ensure → preparing|ready ; direct original: ready immediately
client → GET /downloads/{id}/file → serveContent(source|artifact, Range, throttle)
```

**Register a managed bitrate download (mobile)**
```
POST /downloads {content_id, quality:"5mbps", caps:{...}}  (+X-Silo-Device-Id, +X-Profile-Id)
 → Resolve: cfg.TranscodeEnabled && user.DownloadTranscodeAllowed → delivery_format=transcode
 → upsert downloads (device entry, status=preparing)
 → ArtifactManager.Ensure(media_file_id, params): ON CONFLICT dedup → enqueue encode if new
encode worker → playback.PrepareFile(... -movflags +faststart) → rename → artifact=ready
            → linked rows=ready → events hub notify
client (on notify) → GET /downloads/{id}/file → serveContent(artifact, Range, throttle)
                  → GET /downloads/{id}/manifest → download ArtworkURLs.* and Subtitles[].FetchURL once
                  → PATCH /downloads/{id} {status:"completed"}
```

**Offline progress, reconnect**
```
(offline) client queues {media_item_id, position, duration, updated_at} per stop
(online)  POST /sync/progress {items:[... updated_at ...]} → SetProgressIfNewer per item
          GET  /progress?since=<cursor> → rows changed elsewhere since last sync
```

## Phased build sequence (each phase independently shippable)

- **Phase 0 — Reshape + scaffolding (coordinated web update).** Create `internal/downloads`; reshape the
  `downloads` table (device plus quality/delivery columns, widened status) and the `/downloads` +
  `/direct-download` endpoints to be quality-aware; port `BandwidthManager`/`QuantityLimiter`/serving;
  extend `config.DownloadConfig` (+ new keys, default-off); add quality/delivery constants + sentinel
  errors; add `GET /downloads/capability`. Update the web app's download hooks/components
  (`web/src/hooks/queries/downloads.ts`, `web/src/components/DownloadVersionPicker.tsx`) to the reshaped
  DTOs. Delete `internal/download`.
- **Phase 1 — Managed device entries.** Device-aware create/list/patch/delete; managed-entry serving for
  `original`; `X-Silo-Device-Id` handling + the device-entry unique constraint. **Invariant 2** lands
  here: profile+device authorization on every managed endpoint, header-only `device_id` authority.
  *Acceptance tests:* a second profile on the same `user_id` is denied a managed row's `/file` and
  `PATCH`/`DELETE`; a body/query `device_id` cannot override the header.
- **Phase 2 — Offline manifest.** `ManifestBuilder`; manifest + artwork + subtitle proxy endpoints, each
  under the invariant-2 authorization + a per-profile content-access re-check before serving.
  *Acceptance test:* a child/library-restricted profile is denied artwork/subtitle/manifest for an
  out-of-scope download id.
- **Phase 3 — Prepare-to-file (remux + transcode) [v1-critical].** `download_artifacts` migration (incl.
  lease/attempt columns); `ArtifactManager` as a **durable leased queue** (transactional claim, lease
  heartbeat, startup sweep, `.part` cleanup) per **invariant 3**; `playback.PrepareFile`; encode worker on
  `taskmanager`; ready-state serving; events notification; cleanup job. Turn `download.transcode_enabled`
  on. *Acceptance test:* kill the process mid-encode → on restart the artifact re-enqueues and the linked
  download reaches `ready` (no permanent `preparing`); concurrent workers never double-encode.
- **Phase 4 — Progress reconciliation.** `event_at` (clamped) + server-assigned `synced_seq` columns on
  `watch_progress`; clamped `updated_at` on `POST /sync/progress`; server-token `?since=` on
  `GET /progress`; `ListProgressSince` in both stores + store test suite, per **invariant 1**.
  *Acceptance test:* a future-dated client `updated_at` is clamped (cannot win over a later real write)
  and never advances another device's cursor.

Phase 0 precedes all and is the coordinated server+web reshape. Phases 1/2/4 are independent; Phase 3
depends on 1. Remux can ship in Phase 3 even if transcode stays admin-off.

## Client impact

- **Android (`silo-android`):** persist a stable `device_id` (UUID in app storage) and send
  `X-Silo-Device-Id`; call `/downloads/capability` on login to gate UI; register downloads, poll/listen
  for `ready`, pull the file, store the manifest + artwork + subtitle files locally; play the local file
  with ExoPlayer; queue offline progress with timestamps in a Room table and flush via
  `POST /sync/progress`, then pull deltas with `GET /progress?since=`.
- **Apple (`silo-apple`):** same flow; `device_id` from `identifierForVendor` (or stored UUID on macOS);
  `URLSession` background download against `GET /downloads/{id}/file`; AVFoundation plays the local file;
  offline progress queue drained on reconnect. The manifest's markers/chapters reuse the types already
  used for online playback.
- **Web (`web/`):** updated in lockstep in **Phase 0** — the download hooks/components move to the
  reshaped `/downloads` DTOs (quality/effective quality/delivery fields, new statuses). New admin additions: a
  `download.transcode_enabled` toggle in admin settings, plus the existing per-user
  `download_transcode_allowed` switch in `web/src/pages/AdminUserDetail.tsx`. No offline-library UI on web.

## Security & reliability invariants (must-hold)

These three are correctness requirements, not nice-to-haves; each ships with the acceptance test named in
the phase plan. They were hardened in response to an adversarial design review.

1. **Server-owned sync ordering.** Cross-device delta delivery (`?since=`) is driven only by the
   server-assigned `synced_seq`; the client clock is bounded metadata (`event_at`, clamped to
   `now + skew`) used only for LWW conflict resolution on the caller's own profile. A client-supplied
   timestamp can never advance another reader's cursor or lock in a far-future "win."
2. **Full profile + device authorization.** Every managed-entry endpoint authorizes on
   `(user_id, profile_id, header device_id)` and re-checks per-profile content/library access before
   serving bytes/assets. A download id is never sufficient; `device_id` authority is the header only.
   This holds the "no cross-device visibility" and "no cross-profile leakage" invariants given that
   household profiles share a `user_id`.
3. **Durable artifact recovery.** Artifact preparation is a leased, attempt-counted, transactionally
   claimed queue with a startup sweep, so no crash/restart can strand a download in `preparing` or
   double-encode the same artifact.

## Risks & trade-offs

- **Coordinated server+web reshape (Phase 0).** Reshaping `/downloads` requires the web app to land in
  the same milestone. Mitigated because both are first-party and the reshape is mechanical (added fields,
  not removed behavior for the existing web path).
- **One table, two lifecycles.** A nullable `device_id` plus a wide status enum carries both ephemeral
  and managed rows. Deliberate and managed (partial unique index + MATCH-SIMPLE FK keep the two modes'
  invariants clean); the alternative two-table split was only justified by the now-lifted compat
  constraint.
- **Artifact disk + CPU cost.** Mitigated by `(media_file_id, format, params_hash)` dedup (shared across
  web/devices/users), a bounded encode pool, LRU/byte-budget cleanup, and transcode being **off by
  default** (admin opt-in).
- **Remux is not free for a clean downloadable file.** `+faststart` needs a finalization pass, so even
  remux is a prepare-to-file job. It is fast (`-c copy`) but not instant; `preparing → ready` + push
  notification covers the UX.
- **`content_id` can change on rescan.** The manifest carries `StableIdentity` (provider IDs +
  season/episode) mirroring `WatchIdentity` so the client re-resolves after a catalog rebind; the stale
  row doesn't break the on-device file.
- **No expiry by decision.** A revoked `download_allowed` stops *future* serves (re-checked each serve,
  as today at `internal/download/service.go:345`) but cannot remove an already-downloaded file. Call this
  out in admin copy.
- **LWW vs. client clocks (resolved, see invariant 1).** Conflict resolution uses a clamped `event_at`
  and delivery uses the server-owned `synced_seq`, so a skewed/malicious clock is bounded to "now" on its
  own profile and cannot poison cursors. Residual: an honest-but-wrong client clock within the skew
  window can still mis-order two of *its own* near-simultaneous events — acceptable.
- **Authorization breadth (resolved, see invariant 2).** The per-endpoint profile+device authorization
  and pre-serve access re-check are load-bearing; the acceptance suite must include a cross-profile and a
  cross-device denial test, since a regression here is a content-access leak, not just a bug.
- **`UserStore` gains a method + `watch_progress` gains columns.** `ListProgressSince` plus the
  `event_at`/`synced_seq` columns touch every store implementor/stub and the per-user schema — all in the
  Phase 4 PR.
- **Quota semantics.** v1: all download rows (web + device) count toward the **same** per-user
  `QuantityLimiter`; revisit if too coarse.

## Out of scope (deferred)

Cross-device download visibility; download expiry/lease/DRM; cumulative per-user storage-byte quotas;
push-to-client "your transcode is ready" beyond the existing events hub; server-initiated deletion of
client-side files.

## Appendix — proposal-ready summary (maps to the v1 capability template)

- **Summary:** Mobile apps can download movies/episodes for fully offline playback — choosing original
  or an admin-gated bitrate quality — and watch progress stays consistent when the device reconnects.
  The web app's existing download feature is folded into the same reshaped contract. Remux remains an
  internal delivery format, not a client-facing quality preset.
- **User value / why v1:** Offline playback is table-stakes for mobile media clients (commutes, flights,
  spotty networks) and the client teams need the download + sync contract locked to build against.
  Reshaping the existing download contract now — before lock and before any mobile client ships against
  it — is the right time; the only current consumer is the first-party web app, updated in lockstep.
- **API surface:** the reshaped + new `/downloads/*` endpoints listed in "API surface" above (capability,
  device-aware create/list/patch, file serving, manifest/artwork/subtitle proxies) plus
  `updated_at`/`?since=` on the progress endpoints. **Note for reviewers:** this *changes* the existing
  `/downloads` contract (additive-only is intentionally waived pre-lock for this first-party endpoint).
- **Client impact:** Android — full offline UI + sync; Apple — full offline UI + sync; Web — lockstep
  Phase-0 DTO update + admin toggles.
- **Rough size (server-side):** **L** (new package replacing the old one, table reshape + artifacts
  table, async encode pipeline, manifest assembly, additive progress sync).
