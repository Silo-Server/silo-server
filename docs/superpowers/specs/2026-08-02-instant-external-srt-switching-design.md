# Instant External SRT Switching Design

## Problem

Android TV currently receives only the subtitle artifact selected by playback
protocol V3. Other catalog tracks remain selectable, but they have no mounted
Media3 sidecar. Selecting another external SRT therefore performs a playback
replan and replaces the `MediaItem`. On Shield this flushes the decoder and
briefly displays Media3's buffering indicator even when the replacement route
is otherwise identical.

The Android client already has an exact-identity fast path for selecting a
sidecar that is present in the current Media3 track graph. The missing piece is
an authoritative, safe set of alternative external text artifacts in the
initial V3 plan.

## Decision

Playback protocol V3 will add a negotiated external-text-sidecar set. Android's
local Media3 engine will advertise support for the feature, and the server will
return every usable external SRT or WebVTT track for the effective media file.
Android will mount that set in the initial `MediaItem`, allowing its existing
local track-selection path to switch among those tracks without a replan,
transport replacement, decoder flush, or buffering indicator.

The feature is deliberately narrower than "all subtitles":

- external SRT/SubRip and WebVTT tracks are eligible;
- ASS/SSA remains on the existing selected-artifact path because styling and
  font fidelity require a separate policy;
- embedded text remains on the existing extraction/replan path rather than
  eagerly starting an extractor for every embedded track;
- downloaded subtitles remain on the existing path because their object-store
  availability is not proven by the media-file inventory;
- bitmap subtitles and burn-in routes continue to replan.

This is the smallest change that removes the observed SRT switch disturbance
without expanding startup work to expensive or failure-prone subtitle types.

## Protocol Contract

The shared feature name will be `external_text_sidecar_set_v1`.

Android includes it in both `client_features` and
`client_playback_context.features` for local Media3 playback. Cast and clients
that do not mount Media3 sidecars do not advertise it. The server includes the
same value in `server_features` when it supports the response field.

`playback_plan.subtitle` gains an additive `sidecars` array. Each entry carries:

- the stable V3 track ID;
- the combined subtitle selection index;
- URL;
- MIME type;
- format;
- timeline origin.

The existing singular `artifact` remains authoritative for the currently
selected subtitle. Existing clients can ignore `sidecars`; clients that do not
advertise the feature receive no sidecar set. No existing field changes type or
meaning.

The server attaches the eligible set to initial decisions, replans, and
idempotent decision replays. This keeps replacement plans self-contained and
ensures that switching back from a route-changing subtitle does not lose the
fast-path alternatives.

## Server Artifact Validation

The server builds the set from the frozen external-subtitle inventory of the
effective media file. Before advertising an entry, it reads the external file
and verifies that it is non-empty and in an eligible format. An unreadable,
missing, empty, or unsupported entry is omitted and logged with media-file and
combined-index context; it does not fail playback and cannot poison Media3's
merged source graph.

Eligible SRT files are exposed through a raw `.srt` URL with
`application/x-subrip`; eligible WebVTT files use raw `.vtt` with `text/vtt`.
The subtitle stream handler will serve those requested raw formats directly.
This avoids running or repeating FFmpeg conversion and lets Media3 parse the
small text resources natively. Existing `.vtt` behavior and the selected
artifact contract remain compatible.

Validation is a snapshot guarantee. If an administrator removes a subtitle
file after the plan is issued, the later request may still fail just as any
media resource can disappear after planning; that race is not converted into a
video transport replan.

## Android Data Flow

The shared Kotlin protocol model decodes `subtitle.sidecars`. The V3-to-session
adapter converts each entry to `PlayerSubtitleInfo` using the supplied combined
index and stable identity, then merges the selected singular artifact and
deduplicates by combined index.

The existing catalog merge supplies language, title, forced, default, and SDH
metadata. `SubtitleManager` turns each nonblank eligible URL into a Media3
`SubtitleConfiguration`, and `SiloMediaSourceFactory` mounts the configurations
alongside the primary content before playback starts.

When the user chooses one of these tracks, the view model resolves its exact
stable ID against the current Media3 tracks and applies the existing local
track override. The CC quick picker closes the full HUD as it does today. No
playback-plan publication, position restore, or spinner is involved.

If the server omits the feature or a specific entry, Android retains the
current staged-replan fallback. This preserves interoperability with older
servers and availability for tracks outside the fast-path scope.

## Alternatives Considered

### Return every legacy `subtitle_urls` entry

The legacy endpoint already constructs broad subtitle URLs, but changing V3 to
speculate those URLs would repeat the failure mode that originally caused
catalog-only Android rows to use blank URLs: one stale artifact can prevent the
primary media source from preparing. It would also change behavior for clients
that never requested eager sidecars.

### Hide the Media3 buffering indicator

The indicator reflects a real `MediaItem` replacement and decoder flush.
Suppressing it would leave the interruption and black frame intact while
removing useful feedback for genuine buffering.

### Make Android dynamically add a sidecar after playback starts

Media3 does not provide an atomic operation to add a new child to the prepared
merged source graph. Rebuilding the graph is the remount that causes the visible
disturbance, so this cannot deliver an instant switch.

### Preload every subtitle type

Embedded extraction, object-store downloads, ASS conversion/font handling, and
bitmap processing add startup latency and broaden the failure surface. Those
types need separate caching and fidelity designs and are not required to fix
the observed external SRT case.

## Error Handling and Observability

- Sidecar-set construction is best-effort per track; one rejected entry does
  not fail the playback decision.
- Logs identify the media file, combined index, format, and rejection class
  without exposing subtitle contents or filesystem paths.
- Duplicate combined indexes are discarded deterministically before the
  response and again by Android before mounting.
- Android ignores malformed entries with blank URLs, negative indexes, or
  unsupported MIME types and retains replan behavior for their catalog rows.
- The selected singular artifact remains required whenever the planner chooses
  render or convert mode; sidecar-set failure never weakens that invariant.

## Verification

Server tests will establish that:

1. the feature is advertised and response parsing remains additive;
2. a capable request receives every readable external SRT/VTT entry with raw
   URL, MIME type, stable ID, combined index, and timeline origin;
3. an incapable request receives no sidecar set;
4. missing, empty, unsupported, embedded, downloaded, and bitmap entries are
   excluded without failing playback;
5. start, replan, and idempotent replay responses preserve the set;
6. raw `.srt` and `.vtt` requests serve the expected content type and bytes.

Android tests will establish that:

1. local Media3 playback advertises the feature while Cast does not;
2. the Kotlin protocol models tolerate servers with and without `sidecars`;
3. the V3 adapter merges and deduplicates the set with the selected artifact;
4. all returned external SRT/VTT entries become stable Media3 subtitle tracks;
5. selecting any mounted entry stays on the local commit path and performs no
   staged replan;
6. an omitted or invalid entry still uses the existing replan fallback.

The coordinated change is complete only after both repositories' focused tests
and full verification gates pass, an ARM64 debug APK is installed on Shield
without launching it, and device logs confirm a switch between two external
SRT tracks has no replan, player remount, decoder flush, or buffering state.

## Compatibility and Rollback

The `/api/v1` change is additive and capability-negotiated. Older Android and
Apple clients retain the singular selected artifact. A newer Android client
against an older server sees no sidecar set and uses its existing fallback.

No migration or persisted-data change is required. Either repository can roll
back independently without making playback unusable; rolling back the server
removes the optional set, while rolling back Android leaves the additive field
ignored.
