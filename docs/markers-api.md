# Marker API

The v2 marker API reads effective file markers and updates manual markers. Each
operation applies the existing file, library, item, episode-parent, and extra-parent
access policy. Reads require authentication. Writes also require `marker_edit`
permission and pass the demo and viewer-access gates. Profile context remains
optional; supplying a profile applies its access restrictions. When any profile
on the account is PIN-protected or access-restricted, reads and writes without
`X-Profile-Id` are refused with `422 validation_failed` at
`header.x-profile-id`, so account scope cannot bypass those limits. API keys are
exempt.

## Provider modes and storage

New installations include TheIntroDB and default to online markers with local
detection as a fallback (`markers.mode=both`). Online markers are saved to the
library by default. The setup wizard offers separate controls for online lookup
and local detection before automatic lookup begins. New TV and mixed libraries
created in the web UI enable local detection by default; new movie libraries
leave it off, since movie credits are best effort. Existing library choices are
preserved. Local detection only runs in libraries where it is enabled.
Existing installations retain their configured marker mode, which accepts `off`,
`local`, `online`, and `both`.

`markers.online_storage` chooses how online results are used:

- `stored` persists markers and enables the **Sync online markers** task,
  scheduled daily at 03:00 in server-local time by default.
  Each run lists the due files once: unqueried files first, then the least
  recently fetched results, so a run stopped by a provider quota resumes where
  it left off. A run, including one that starts during a cooldown, waits out a
  cooldown of up to a minute that covers every provider and ends at a longer
  one. A metadata change makes a file due until a sync or lookup confirms its
  external IDs against every provider.
  For every provider, successful responses are fresh for 30 days and empty
  results are retried after 14 days, following TheIntroDB's guidance for
  clients that sync a library. Episodes that aired, and movies released, in the
  last 30 days or the coming week use 7 days and 1 day instead, while
  providers are still gaining markers for them.
  `markers.lazy_playback` also allows reads and playback to fill missing markers.
- `on_demand` looks up markers for the selected file and keeps a bounded,
  fifteen-minute memory cache. It does not persist provider responses or run
  background synchronization. Request leases, failures, and quota cooldowns are
  still shared through the database. Previously stored markers remain available.

Both paths honor provider selection, manual edits and deletions, and provider quota limits.

When several providers return a marker of the same kind, Silo uses the
provider whose best marker of that kind has the highest confidence, then the
lower `fetch_priority`, then the higher submission count, then the provider ID,
and keeps every range that provider returned for the kind. A marker without a
confidence, or with a non-finite one or one outside 0 to 1, counts as 0, so it
loses to any rated marker. Selection runs over cached answers, so a
priority change applies without fetching again.

Each provider has a minimum confidence for lookups, `fetch_min_confidence` on
the v2 provider configuration, default 0. A marker the provider rates below it is
left out, so another provider or local detection fills its kind,
and a stored marker of that provider for the kind is withdrawn, even when the
provider's answer comes from the cache. A marker without a confidence counts as
0. The minimum is applied when results are selected, so a change reaches cached
answers the next time the file is looked up or refreshed, without fetching
again. The **Sync online markers** task looks up only files whose cached answer
is due, so with `markers.lazy_playback` off a raised minimum reaches a file with
a fresh answer when that answer comes due, up to 30 days later, or on an admin
refresh. A provider's confidence is on its own scale, not local detection's, which
is why the default keeps every marker.

Online markers outrank local ones, with one exception. A marker placed from
the file's own chapters, an intro or credits chapter with or without a silence
extension, is compared with an online marker by confidence: an online marker
rated below it does not replace it, and local detection replaces an online
marker rated below the file's chapter. A tie goes to the online marker.
Chapter-derived local markers are rated 0.95, or 0.98 with a silence
extension. Chromaprint, audio, and video results and copies from another version
of the episode are not compared this way, so any online marker still replaces
them.

Providers address an episode by its series' TMDB, TVDB, and IMDb IDs and the
catalog's season and episode numbers; season 0 holds specials. When the catalog
has them, lookups and contributions also carry the episode's own provider IDs,
its title, and whose numbering it follows. Episode rows merge metadata
providers' episodes by number in the order of the library's episode chain, so
the numbering is that of the first TVDB or TMDB provider in the chain whose ID
the episode carries. Automatic sharing skips specials; sharing a manually
edited special's markers is allowed.

Automatic sharing selects an episode file only while some provider would
submit it: the marker meets that provider's minimum confidence, the series has
every external ID the provider requires, and the provider has not already
claimed the episode. A file that every provider declines is not selected again
until one of those inputs changes, for example when the series gains a TMDB ID.

A provider whose marker capability declares `supports_amend` replaces its
earlier submission instead. It holds one submission per file, and a file is
selected again whenever the marker's content hash changes: new bounds, source,
detection version, or confidence (to the thousandth), a re-probed duration, a
new file hash or size, or a change to the series or episode IDs or the episode
numbering. The daily task
also resends to such a provider a manual correction that the save did not
deliver, when the provider already holds a submission for that file and kind.

The daily **Share detected markers** task is scheduled at 04:00 in server-local
time by default, after local detection at 03:30. Each scheduled run starts at a
random point in the four hours after its configured time, so servers in one
time zone do not reach a shared provider together; a manual run starts at once.
A run waits out a provider's short rate limit and ends at a longer one. When
every submission for a file fails, the run waits 30 seconds before the next
file, doubling for each further failed file in a row up to 10 minutes. Any
provider answer, a refusal included, resets the wait. After the ninth failed
file in a row the run ends, and the next scheduled run tries again.

Contribution is off for a new provider unless its plugin manifest asks
otherwise. The capability metadata keys `default_contribute_enabled`,
`default_contribute_auto_local`, and `default_contribute_min_confidence` set the
provider's contribution settings when its config row is first created; later
admin changes stick, and a reinstall starts from the defaults again.
`default_contribute_enabled` on the v2 provider configuration reports a provider
whose manifest turns sharing on. The server setting `markers.share_by_default`
(`true` or `false`, default `true`, set by the setup wizard) decides whether
those defaults apply: when it is `false`, new providers start with sharing off,
and changing it to `false` also turns contribution and automatic sharing off
for existing providers whose manifest turns sharing on. That change commits
with the setting: if it fails, the save fails and the setting keeps its old
value. Changing it back to `true` changes no existing provider. Every
contribution, whether from the automatic run, a manual marker save, or an admin
request, reads the stored provider config first, so a node that missed the
change event still stops; if that read fails, nothing is shared. The admin
provider list reads the stored rows too, and an admin edit writes only the
fields its request sends; the web provider tile sends only the fields the admin
changed, so editing another setting cannot turn sharing back on from a stale
copy.

A manual save is never shared for a kind it may copy from a provider: both
bounds within 1 s of any occurrence of an online or plugin marker stored on
the file, or of an earlier save withheld this way. With
`markers.online_storage` set to `on_demand`, provider markers are not stored,
so every kind a save sets is withheld unless the file already held that kind
as a `manual:v1` marker. A withheld kind is
stored with algorithm `manual:withheld:v1` and otherwise acts as a manual
marker.

A manual deletion removes every occurrence of that kind for the selected file.
The empty range retains manual provenance: provider lookups, local detection,
rescans, and file-identity changes cannot restore it. An explicit manual set
adds the kind again. Other marker kinds are unchanged.

Manual writes require a Movie or Series library, including their mixed-library
variants. Unsupported library kinds return `422 validation_failed` without
changing markers, contributing them, or emitting a playback update.

Local detection finds episode intros and end credits, and movie end credits on
a best-effort basis: from chapters and the picture near the end, never intros.
Some movies get no local credits, or credits that start late. It never replaces
a marker from a higher-priority source, except an online marker rated below
the file's own chapter, and it judges each kind separately, so an episode with
an online intro can still get local credits. With `on_demand`
storage, players keep the online intro while playback detects the credits.

`markers.detect_intros` and `markers.detect_credits` choose which kinds local
detection finds, server-wide. Each is `true` or `false`, independent of the
other, and defaults to `true`; a change applies without a restart. Credits
detection costs more, since it reads the end of each episode and movie. With
credits off, the **Detect markers on this server** task, playback detection,
and admin refresh skip episode credits and movies; with intros off, they skip
episode intros. Chapter markers of a kind that is off are skipped as well.
Turning a kind off keeps the markers already saved.
`detection_kind_settings` on `GET /api/v2/admin/markers/capabilities` reports
that the server honors the two settings; an older server stores them through
the generic settings endpoint but ignores them, so clients offer the switches
only when it is `true`.

`markers.detection_workers` sizes local detection: how many seasons or movies
the **Detect markers on this server** task analyzes at once, which also bounds
how many ffmpeg processes read audio and video. It defaults to `1` and accepts
1 to 64.
Detection mostly waits on reading each file's opening and closing minutes, so
a higher value finishes a large library sooner on fast storage, at the cost of
load that competes with playback. Analysis started from playback always has one
extra ffmpeg slot of its own. A change applies without a restart; extractions
already running finish first.

Replicas coordinate fetches with expiring database leases. File replacement or
rematching invalidates derived markers; a result fetched for the previous file
identity cannot overwrite the new one. Successful provider refreshes can correct
or withdraw that provider's existing ranges.

`POST /api/v2/admin/items/{id}/refresh-markers` explicitly refreshes an episode
or a movie from its configured sources; `movie_credits` on
`GET /api/v2/admin/markers/capabilities` reports that movies are accepted. In
`both` mode, eligible local detection fills missing intro and credits markers
of an episode, or missing credits of a movie, for the kinds the two detection
settings leave on.
`POST /api/v2/admin/items/{id}/redetect-markers`, which `redetect_markers` on
the same capabilities document reports, reruns local detection of an episode's
intro, credits, or both, or of a movie's credits; see
[admin-catalog-api.md](admin-catalog-api.md#episode-and-movie-marker-analysis).
The v1 refresh endpoint retains its local-only behavior, and it, the v1
re-detect endpoint, and v2 `redetect-intro` analyze episode intros only,
whatever the detection settings say.

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
`algorithm`, `detected_at`) are omitted when absent. A manual save stores
algorithm `manual:v1`, or `manual:withheld:v1` for a kind withheld from
sharing. Timestamps use the shared v2 instant format.

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

Credits from every source follow the placement local detection uses, because
players open Up Next at the credits start. When the duration is known, credits
must start in the file's tail window (for an episode the last 450 seconds or
40% of the file, whichever is shorter; for anything else the last 900 seconds
or 25%), at or after the end of the intro, and run from 15 seconds up to the
window's seconds limit (450 or 900). PUT rejects credits that fail this and
names the rule they broke. Provider, sidecar, and on-demand results drop those
occurrences instead, and a refreshed provider that returns only misplaced
credits loses its stored ones. `markers.CreditsLimits` holds the rule, and `markers.CreditsLimitsForFile` picks
the episode or movie bounds for a file.

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

### Marker preview images (v2)

The `marker_thumbnails_v1` playback capability advertises optional `thumbnail_url`,
`thumbnail_thumbhash` and `thumbnail_capture_seconds` fields on effective marker
occurrences in watch and marker reads. Absent images omit these fields. Capture
seconds equal the occurrence's start; navigation continues to use its unchanged
start/end bounds. Embedded chapter images remain a separate inventory.

The existing per-library chapter-thumbnail setting controls both chapter and
marker extraction. Marker-only files are eligible. Effective occurrences include
manual, provider and legacy ranges, including repeated kinds. Extraction uses
the existing hardware, execution-mode, HDR, width and retry policies. A failed
preview never prevents marker navigation or playback. Internal image metadata
is bound to the file identity and exact occurrence; concurrent edits cannot
publish a stale frame. The frozen v1 responses do not gain preview fields.

On-demand provider ranges remain ephemeral. When the library enables previews,
lookup queues thumbnails from the effective ranges and stores only opaque
occurrence identities and image/retry metadata. Worker snapshots expire after
15 minutes; after expiry or an API replica restart, a new on-demand lookup is
required to generate or resize provider-only previews. Background scans do not
fetch providers or persist their navigation ranges. A newer lookup or a manual
range edit fences extraction results from the previous snapshot.
