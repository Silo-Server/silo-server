# 2026-07-17 — Embedded subtitle extraction: investigation and fix

Commands assume the repository root is the cwd.

This document records both the **final design** and the **investigation that
produced it**, including four superseded revisions. The dead ends are kept
deliberately: every one of them looked correct, three were disproved only by
measurement, and the last was a release-blocking bug caught in review. Anyone
tempted to "clean up" the subtitle windowing code should read Evolution first.

## Summary

Extracting an embedded subtitle track walks the interleaved container: subtitle
packets sit between video and audio across clusters, so harvesting a few KB of
text means demuxing that stretch of a multi-GB file off network storage (CephFS).

The server believes it bounds this with a 600s window. **It does not** — the flag
is inert, so every request extracts from the seek point to end-of-file. We cannot
turn the window on, because two of three clients depend on receiving whole tracks.

**Fix: cache the extract. Change nothing a client can observe.**

## Evidence

41.6h of production logs (2026-07-15 09:19 → 2026-07-17 02:56, 1.29M requests),
`/api/v1/stream/{session_id}/subtitles/{track}`:

| client | requests | p50 | p95 | max | ≥10s |
|---|---|---|---|---|---|
| browser (web player) | 220 | 72ms | **120,021ms** | 121,470ms | **45** |
| native (Android/TV) | 42 | 145ms | 9,566ms | 41,607ms | 2 |

Sidecar/external subtitles: **118 requests, all <1s** — standalone files, read
directly, no demux. That fast path already exists and is why the distribution
first looked bimodal.

Both clients are slow; they differ in *how often they pay*. The web player
re-fetches a window every ~10 minutes of playback, so each slow fetch **stalls
subtitles mid-film** — that is the 120s cluster. Native fetches once per track and
holds all cues, so it rolls the dice once: up to ~41s before subtitles appear, then
fine.

The 120s ceiling is `WriteTimeout: 120 * time.Second` (`cmd/silo/main.go:2389`)
cutting the connection mid-body. `WriteHeader(200)` was already called
(`stream.go:576`) and the logging middleware records that status
(`internal/api/middleware/request_logger.go:29`) — so **truncated subtitles are
logged `status=200`** and are invisible in error metrics.

## Root cause: the window flag is inert

`streamExtractArgs` (`subtitle_stream.go:161-168`) passes `-t` as an **input**
option, reasoning it "caps how much of the file we read". Measured against the
production binary (`ffmpeg 7.1.4-Jellyfin` at `/usr/lib/jellyfin-ffmpeg/ffmpeg`
inside the `silo` container — not on `$PATH`; there is no host ffmpeg):

| command | wall | bytes | last cue |
|---|---|---|---|
| `-ss 4000 -t 30 -i F` | 5s | 38,562 | **02:10:04** |
| `-ss 4000 -t 300 -i F` | 6s | 38,562 | **02:10:04** |
| `-ss 4000 -i F` (no `-t`) | — | 38,562 | 02:10:04 |
| `-t 600 -i F` (seek 0) | **86s** | 83,092 | **02:10:04** |

`-t 30` and `-t 300` are byte-identical to each other **and to passing no `-t` at
all**. Only `-ss` works. Cost is therefore *(duration − seek) × bitrate*, which the
logs confirm exactly: `seek=8s/130s/519s` → ≥60s; `seek=7797s/8392s/8987s` → <1s.
Bitrate correlates monotonically (39.8 Mbps → 120.6s; 5.0 Mbps → 8.0s; every 120s
outlier is a ≥28 Mbps remux).

`-to` as an **output** option does bound it correctly — `-ss 4000 … -to 4600` →
1s, 9,170 bytes, last cue 01:16:32. **We are deliberately not using it.**

## Why the window stays off

Subagent scans of the sibling repos found the accidental whole-track behaviour has
become the de-facto contract:

- **silo-apple — BREAKS.** `SidecarSubtitleFetcher.swift:48-95` issues a single
  GET; `SubtitleSession.swift:244-287` caches the whole response in
  `sidecarCache[urlIndex]` and never re-requests. No `position=`/`duration=`, no
  window constant, no sliding window. Its comment states the "buffered path" is
  deliberate.
- **silo-android — BREAKS.** `SubtitleTrackMerge.kt:56` builds the URL bare and
  hands it to Media3 as a `MediaItem.SubtitleConfiguration`
  (`SiloPlayerFactory.kt:413`) → `ProgressiveMediaSource` fetches once, parses every
  cue. No timer, no position listener, no re-fetch.
- **web** windows correctly: sends `position=`
  (`web/src/player/hooks/useSubtitleTracks.ts:79`), advances by 595
  (`WINDOW_DURATION 600 − WINDOW_OVERLAP 5`, `:9`/`:16`/`:409-410`) — the exact
  stride observed in production.

Enabling the window would silently kill subtitles ~10 minutes into every film on
iOS, tvOS, macOS and Android. Caching removes the need: keep whole-track delivery,
make it cheap.

## Design

`SubtitleCache` (`internal/playback/subtitle_cache.go`) already implements the
needed shape for PGS: a full-track miss streams to the client while teeing into a
temp file, atomically renamed on clean exit (`ServeSUPExtract:113`); a hit serves
from disk; a windowed request re-extracts from the small cached artifact
(`serveWindowedSUP:161`, via `InputIsExtractedSup`) or starts `WarmInBackground:188`.
Keyed by path + track ordinal + source mtime+size. Text was excluded only by the
assumption at `stream.go:557-562` that "VTT is already windowed and fast" — which
the inert `-t` makes false.

Windowing a **cached** 83 KB VTT costs **52ms** vs 16,000ms against the original
27 GB remux (`-ss 4000 -i cached.vtt … -to 4600` → 9,216 bytes, cues 01:04:40 →
01:16:32). Note `-ss` on a webvtt input lands ~2 min *before* the target — a
coverage superset, not byte-equivalence.

### The load-bearing rule: canonical artifact + effective-partial predicate

A cache entry may only ever hold a **canonical extract**: source track → requested
output format, `seek=0`, `duration=0`, original-container input, original track
mapping.

**`AllowWindow` must not be used to decide this.** It is set only in the PGS branch
(`stream.go:531`), so it is always false for text — while `streamExtractArgs:155`
applies `-ss` to any non-ASS/non-PGS source *regardless* of it. Routing on
`AllowWindow` therefore lets a seeked text extract be committed as canonical:

1. subrip request at 900s → `AllowWindow=false` → full-track path.
2. ffmpeg applies `-ss 900`; inert `-t` does not save it → output is 900s→EOF.
3. ffmpeg exits cleanly → `Commit` publishes the partial as canonical.
4. Every later viewer from 0 gets a hit and **receives no cues before 900s**.

118 of 143 production requests carry a non-zero seek, so this would poison
immediately and silently truncate subtitles for everyone until eviction. Classify
from the **effective argv** instead — ideally via the same helper that builds it:

- effective seek/duration ⇒ partial ⇒ **never fills** the canonical entry; on a hit
  re-extract from the cached artifact, on a miss stream the seeked extract uncached
  and start a canonical background warm.
- seek=0 & duration=0 ⇒ canonical ⇒ may fill.

(ASS→VTT happens to be safe because `IsASS` blocks `-ss` — that does **not**
generalize to subrip, which is 135 of 143 requests.)

### Steps

1. `subtitle_stream.go` — export the output-format resolver (`streamExtractOutput`
   already is one; export it). Do **not** add a second mapping: the handler's
   duplicate at `stream.go:493-499` is exactly what let two copies disagree.
   Preserve current windowing behaviour **exactly** — this commit changes latency
   only.
2. `stream.go` — delete the duplicated mapping; resolve format once. Keep
   `seek`/`duration` selection keyed so the log line cannot report a window ffmpeg
   will ignore.
3. `subtitle_cache.go` — generalize:
   - **Key**: add the resolved output profile (out codec + muxer, not the caller's
     `"vtt"` string) **and a schema version** — mtime+size cannot detect an in-place
     ffmpeg upgrade or a future argv change. `.ass` and `.vtt` are both reachable
     for one ASS source, so format must be in the key.
   - **`removeStaleSiblings:468` groups by a prefix excluding format** — committing
     `.vtt` would delete a valid `.ass` sibling. Cleanup (`:476`) and eviction
     (`:517`) hardcode `.sup`, so text would never be reclaimed or counted.
   - Generalize `InputIsExtractedSup` (used at `subtitle_stream.go:170-176` to force
     `-f sup` + remap `0:0`) to carry the cached input's format.
   - `serveWindowedSUP:171` hardcodes `application/octet-stream`; drive Content-Type
     from the resolved format (`text/vtt`, `text/x-ssa`).
   - Rename now that it carries text: `SUPExtractFunc` → `SubtitleExtractFunc`,
     `ServeSUPExtract` → `ServeExtract`, `serveWindowedSUP` → `serveWindowed`.
   - In-flight coalescing must include the output profile.
   - Fix the doc comment (`:19-38`) and the `defaultSubtitleCacheMaxBytes` sizing
     note, which both claim PGS-only.
4. `stream.go:563` — remove the `outFormat == "sup"` gate so text routes through the
   cache. Correct the false comment at `:557-562`.
5. **Keep HTTP semantics unchanged**: copy the cached file with the current
   Content-Type and `no-store`. Do **not** adopt `http.ServeContent` here — it adds
   Range/`206`/`304`/`Content-Length`, and Media3 uses range-capable data sources,
   so responses would differ depending on whether the cache happened to be warm.
   Revisit separately with client testing.

## Risk / follow-ups

- **The inert `-t` stays.** Removing or "fixing" it changes what clients receive.
  Its comment (`subtitle_stream.go:161-165`) confidently describes a bound that does
  not exist and **must be corrected in place** — otherwise the next reader fixes it
  and breaks both native clients. This is the most important comment in the change.
- **Cache-key completeness is correctness-critical.** A missing dimension serves
  wrong bytes. mtime+size remains a residual risk (a replaced file with identical
  size and preserved timestamp reuses stale content); `MediaFile.FileHash` exists if
  we want content identity later. Documented, not solved.
- **First-touch is unimproved.** Native clients fetch once, so the first viewer of a
  file+track still pays full price; they benefit only from another viewer's warm.
  The web player's 2nd–14th windows are the win.
- **Cached-vs-cold output is a coverage superset, not byte-identical** (`-ss` on a
  webvtt input lands early). Verification standard is semantic cue coverage.
- **120s WriteTimeout masking is not fixed** (`status=200` on a truncated body) —
  largely inherent once headers are committed;
  `LogSubtitleStreamError` (`subtitle_stream.go:273-277`) documents it as
  deliberate. A cold first extract of a large remux can still exceed it. Follow-up:
  the rolling deadline full downloads already use
  (`internal/api/handlers/downloads.go:402`, `internal/httpstream/rolling_deadline.go`).
- **Not in scope:** the standalone proxy has separate uncached ASS/text extraction
  paths (`internal/proxy/server.go:273`, `:286`); if remote/proxy subtitle URLs reach
  those, they stay uncached.
- **Dropped: warm-on-playback-start.** `BeginFill` coalesces cache *writes* but does
  not share extraction *output* — so a warm at session start plus the client's
  immediate request (exactly what auto-enabled subtitles do) yields **two whole-file
  demuxes and no latency benefit**. `WarmInBackground:188` also does `stat`/`mkdir`/
  temp-file work synchronously before detaching, which could block session start on
  CephFS. Revisit only if commit 1's metrics show a real gap between playback start
  and subtitle selection.
- **Deferred:** (a) real windowing, once `silo-android`/`silo-apple` send
  `position=`/`duration=` and re-fetch on coverage exhaustion — then `-t`→`-to`
  becomes safe and this cost drops again; (b) scan-time extraction of embedded tracks
  into sidecars, putting every subtitle on the already-instant external path and
  retiring on-demand extraction entirely. (b) is the real endgame.
- **Non-finite `?position=`** (`+Inf` parses and passes `>= 0`, reaching ffmpeg and
  failing after the 200) is a genuine bug found en route — its own commit, not this.

## Evolution (what we got wrong, and how it was caught)

Kept because each error is one a careful reader would repeat.

**R1 — "ASS→VTT skips windowing."** `streamExtractArgs:155` keys `windowable` on
`SourceCodec`, so an ASS source requested as `.vtt` never seeks → full-file demux.
Real, and the code reads exactly that way. Proposed a one-line predicate fix.
*Caught by Codex:* a **no-op**. The handler computes `seek`/`duration` from a
source-derived format *before* the `.vtt` override (`stream.go:493-520`), so ASS
yields `seek=0, duration=0` and no `-ss`/`-t` is emitted whatever the predicate
says. The proposed unit test would have passed while production stayed broken —
it hand-built opts the handler never produces.

**R2 — fix both layers.** Correct, and Codex's implementation was clean.
*Caught by the logs:* the premise was wrong. The `subtitle stream requested` line
records `track_codec`/`seek`/`duration`; correlating it against request durations
showed **all 7 ASS requests logged `seek=0 dur=0` but never exceeded 10s**, while
**every request ≥60s was subrip with `seek=NNN dur=600`** — already windowed. The
same file (71458) ranged 41ms→120,574ms. The tidy "bimodality splits by codec"
story was a subagent's inference that I propagated without testing it against logs
I already had open.

**R3 — `-t`→`-to` plus 600s→60s.** Found by running ffmpeg instead of reading it:
`-t` is inert. This is the true root cause, and it hits subrip (135 of 143
requests), not ASS. *Caught by the client scans:* both native clients fetch once and
expect whole tracks, so enabling the window breaks them. Also caught: 600→60 would
have been worse — the web player hardcodes `WINDOW_DURATION = 600` and advances by
595 without checking what it received, so a 60s server window means a **535-second
subtitle blackout** between fetches.

**R4 — cache text tracks.** *Caught by Codex:* routing on `AllowWindow` lets a
seeked extract be committed as canonical → cache poisoning (see Design). I had
generalized from ASS, the one case where `IsASS` blocks `-ss`.

**Method errors worth naming:**
- I twice reported findings from a subagent's code reading without checking them
  against production data I already had.
- I greped `position=`/`duration=`/`windowed=1` on `/api/v1` request logs and
  concluded no client sends them. **The api request log has no `query=` field** —
  the greps were vacuous. Ground truth is `seek_seconds` in the handler log (118 of
  143 non-zero). Do not grep query params off api log lines.
- The first "86s → 1s" measurement was cache-flattered; the honest cold number is
  16s. Always re-measure cold, in a fresh order.
- Two rounds of code review missed the inert `-t` because we both read *intent*:
  the comment says it windows, the test asserts the flag is present, and it is
  present. Only execution disproved it.

## Verification

- `go build ./...`, `go vet ./...`, `gofmt -l` clean.
- `go test -race ./internal/playback/ ./internal/api/handlers/`. Note
  `TestHandleReplanPlaybackV3SeekFailureRecoveryNeverChangesMediaVersion` **already
  fails on clean `origin/main`** — pre-existing, unrelated, do not chase it.
- Required tests:
  - **a seeked `.vtt` miss must not commit a canonical entry**, and a subsequent
    seek-0 request must still receive beginning-of-track cues (the poisoning
    regression — the single most important test here);
  - seek-0 fill, then a seeked request re-extracts from the cached artifact (assert
    the rewritten input path/format, not the original);
  - `.ass` and `.vtt` for the same file+track coexist and do not evict each other
    via `removeStaleSiblings`;
  - text entries participate in eviction accounting;
  - a partial/failed extract never commits;
  - Content-Type per format on hit and miss.
- **Behavioural equivalence is the bar**: for every (source codec, requested format,
  seek), the bytes a client receives must be unchanged from `origin/main`. Only
  latency may differ.
- Post-deploy: re-run the ≥1s aggregation over `docker logs silo`. Expect browser
  p95 to fall away from 120s and the ≥10s count (45) to collapse toward the handful
  of cold first-touches. Watch `subtitle cache warm` lines for failures and
  dropped-slot rates.

## AI-use disclosure

Investigation, root-cause analysis, and this document were prepared with AI (Claude
Code) assistance. The plan was adversarially reviewed by Codex (`gpt-5.6-sol`),
which caught both the R1 no-op and the R4 cache-poisoning bug; client impact was
assessed by subagent scans of `silo-apple` and `silo-android`.
