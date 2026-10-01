# Trickplay

Trickplay previews are the thumbnails a player shows while the viewer hovers
over or drags the seek bar. Silo generates them per media file, for the video
libraries whose `trickplay_enabled` setting is on (off by default), as JPEG
sprite sheets of thumbnails, and publishes a manifest that says how to cut
them. `internal/trickplay` owns the queue, the manifests, and the storage
lifecycle; `internal/mediasample` decodes and tiles the frames (see
[media sampling](media-sampling.md#sheets)).

## Library setting

A library opts in through `trickplay_enabled` on the v2 library resources
(`createLibrary`, `updateLibrary`); `trickplay_supported` reports whether the
server can generate them (an assets store is configured), and
`GET /api/v2/libraries/capabilities` reports `trickplay: true`. The frozen
v1 library bodies neither read nor write it. Turning it on or off runs a
reconcile pass right away on the server that handled the change; turning it
off deletes the library's previews.

## The row

`media_file_trickplay` holds one row per media file of an opted-in library.
The row is both the file's place in the work queue and its published
manifest.

| State | Meaning |
|---|---|
| `pending` | Waiting for `available_at`, then for a server to claim it. |
| `running` | Leased by `lease_owner` until `lease_expires_at`. |
| `ready` | The last generation published. |
| `unusable` | The file cannot yield sheets (`no_stream`, `invalid_data`); it waits until the file changes. |

A row in any state may also carry a published manifest (`revision` and the
sheet geometry): a regeneration keeps serving the previous sheets until its
own publish.

## Claiming and leases

Every server works the queue; the work per file is independent, so nothing
serializes it across servers.

- A claim takes the due `pending` row that has waited longest with
  `FOR UPDATE SKIP LOCKED`, and leases it with a fresh attempt token stored
  in `lease_owner`. It records the file's size, hash, and duration at that
  moment (`source_*`).
- The worker renews its lease while it runs. A lease that runs out, because
  its server died or stalled, is reclaimed by the next reconcile on any
  server: the row returns to `pending` as a failure and backs off.
- Uploads go under a revision chosen just before the first upload
  (`work_revision`). Revisions are random positive 63-bit numbers, never
  sequence values: they name storage prefixes served as immutable, so they
  must not repeat after a database restore.
- Publishing and finishing are fenced on the lease: they lock the row and act
  only while that attempt token still holds it in `running`. Heartbeats and
  uploads require the same token, so reclaiming a job on the same API server
  cannot authorize an earlier attempt. A worker that lost its lease changes
  nothing.
- A failure backs off 15 minutes, 1 hour, 6 hours, 1 day, then a week. A
  permanent cause marks the row `unusable`. A cause that is the server's own
  (a shutdown, no transcode node to run on) returns the row without counting
  a failure.

`recipe_version` is the newest generation algorithm (`AlgorithmVersion`)
that has touched a row. A server claims, requeues, and invalidates only rows
at or below its own version, so servers on different versions during a
rolling upgrade never undo each other's work.

## Reconcile

A reconcile pass, on any server, brings the queue in line with the catalog,
each step bounded to 5,000 rows. Full batches continue immediately. A pass
yields the cluster lock after four batches and schedules a continuation,
so a large library drains without waiting for the next scheduled task:

1. Reclaims expired leases, as above.
2. Adds the probed video files (with a duration and a video stream) of
   opted-in libraries that have no row.
3. Deletes the rows of libraries that opted out; their sheets are queued for
   deletion.
4. Requeues finished rows whose sheets no longer fit: made by an older
   algorithm, with other settings (`published_recipe`), in another store, or
   from a different file. An `unusable` row is requeued when its file
   changes.

## Generation

Every API server runs a `trickplay.Service`. It claims files while it has a
free worker slot (`playback.trickplay_workers`, default 1, applied live),
and runs each file at idle CPU and I/O priority:

- The recipe comes from the settings in force at claim time:
  `playback.preview_image_width` (default 300, shared with chapter
  thumbnails) and `playback.trickplay_interval_seconds` (default 10, at least
  5). A width up to 320 gets a 10x10 grid; wider thumbnails get fewer tiles
  so a sheet stays at most 3200 pixels wide. JPEG quality is fixed at 80.
- A tile's height follows the display aspect ratio read from the execution
  probe, including a 90-degree display matrix, rounded to an even number.
  The manifest records the extractor's actual tile height; chunks with
  different heights fail without publishing.
- Thumbnail k is sampled at the middle of its interval,
  `k*interval + interval/2`, the last one a second inside the file, so it
  shows what plays in `[k*interval, (k+1)*interval)`.
- The first `mediasample` Sheets request covers one sheet to learn actual
  display geometry. Later requests cover at most 16 sheets and 64 million
  pixels, keeping high-entropy JPEG responses within the bounded node limit; a longer file
  takes several runs, which bounds each run's memory. Each run seeks to the
  keyframe before each sample rather than reading the whole file
  (`Samples`); see [media sampling](media-sampling.md#sheets). AVI files,
  whose index seeks poorly, are read in one pass (`ReadThrough`), and
  containers without a keyframe index (MPEG-TS) always are.
- HDR and Dolby Vision sources are tone mapped (`tonemap.NeedsToneMap`),
  after scaling, so software tone mapping is always allowed.
- Hardware decode follows `playback.hw_accel` and `playback.hw_device`
  (QSV, VAAPI, VideoToolbox), with a software attempt after a hardware
  failure. Attempt timeouts grow with the samples: two minutes plus half a
  second a sample on hardware, two seconds a sample in software.
- A file selected for local extraction that this server cannot read
  (an offline mount) is given back for an hour without counting a failure;
  `invalid_data` and `no_stream` mark it unusable; any other failure counts and backs off.
- ffmpeg runs are recorded under the `trickplay` subprocess workload.

The Queue Seek Previews task runs a reconcile pass at startup and every 15
minutes, on one server at a time. A server also reconciles as soon as it
reads a new width or interval (it rereads the settings every minute), so a
settings change does not wait for the next pass.

## Serving

A manifest is served only while all of these hold:

- its library still has `trickplay_enabled` on;
- the row has a published `revision`;
- `store_identity` is the assets store's current identity;
- the file is the one the sheets were made from: the same size, the same
  hash when both sides have one (a hash may be computed later or not at
  all), and a duration within two seconds (probes round differently).

Sheets are stored at `trickplay/<media_files.id>/<revision>/<index>.<revision>.jpg`
in the assets store. The revision repeats in the file name so that
`artworkkey.Revision` reads it: the artwork route then serves sheets as
immutable, and signed sheet URLs stay stable for a day.

## Reading previews

Players read previews through the native API:

- `getWatchState` marks each version `trickplay_available`, from one batched
  query over the item's files (`catalog.TrickplayAvailability`); batch detail
  reads share a lookup over all files on the page. A failed
  lookup leaves every version without previews rather than failing the
  detail.
- `GET /api/v2/watch/{id}/trickplay?file_id=` returns one file's manifest:
  the interval in milliseconds, the thumbnail size, the grid, the thumbnail
  count, one signed URL per sheet, and when those URLs expire. It serves only
  a file among the versions the watch detail lists for the caller, so it
  applies the same access rules. Every sheet is signed or the manifest is
  withheld: clients cut thumbnails by index and cannot skip a sheet.
  Local storage and S3 with a separate public or token-authenticated delivery
  endpoint use signed `/api/v2/artwork/...` URLs. The server reads sheets from
  the storage API, so delivery lag cannot hide newly published sheets.
  Standard S3 delivery retains direct presigned URLs. Issued URLs protect the
  revision through their latest expiry; clients read the manifest again after
  `expires_at`.
- Playback capabilities advertise `trickplay_v1`.

Thumbnail i shows `[i*interval_ms, (i+1)*interval_ms)`; it sits on sheet
`i / (tile_columns*tile_rows)`, at column `i % tile_columns` and row
`(i % (tile_columns*tile_rows)) / tile_columns` of that sheet.

## Deletion

Sheets are deleted per revision through `blob_gc_queue` (see
[blob storage](blob-storage.md)):

- Publishing queues the revision it displaces; finishing without publishing,
  and reclaiming an expired lease, queue the revision the work had started
  uploading. Both happen in the transaction that changes the row.
- A trigger queues the revisions of every deleted row, whether its library
  opted out or its media file was deleted (the cascade).
- Displaced and abandoned revisions wait at least 48 hours. Before serving
  a manifest, the reader protects its exact published revision until the
  latest expiry of all signed sheets. Publication and deletion carry that
  recorded expiry into the queue, so already issued URLs keep working
  across regeneration, opt-out, deletion, and resolver setting changes. A
  revision replaced while signing is withheld.
- The collector deletes a revision only while no row publishes it and no
  running generation uploads under it; the orphan sweep catches revisions
  nothing queued.

The orphan sweep persists each namespace's continuation cursor under the
store identity, so a bounded run resumes rather than repeating the first
pages. A prefix split across runs carries its newest object time and stays
unqueued until the full prefix has been listed. Errors or an anomaly keep
the prior checkpoint; finishing a namespace clears its cursor, and changing
stores starts a new walk.
