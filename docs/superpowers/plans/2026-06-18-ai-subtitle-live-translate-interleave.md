# Plan: interleave `transcribe_translate` so live AI subtitles follow the playhead

Issue: https://github.com/Silo-Server/silo-server/issues/154
Area: `internal/subtitles/ai` (+ `internal/playback` extraction, `internal/config`)
Type: bug fix to an existing (shipped) capability — makes the chained AI-subtitle
path deliver its designed live behavior. No new client-facing contract.

Commands assume the repository root is the cwd.

## Problem (confirmed)

`Service.runTranscribe` (`internal/subtitles/ai/service.go`) runs the chained
`transcribe_translate` kind as two **global, sequential** stages:

1. Transcribe the **entire** file (progress 5%→70%). Transcript cues are not
   streamed for the chained kind —
   `streamTranscript := streaming && job.Kind == JobKindTranscribe`
   (`service.go:417`), so it is `false` for `transcribe_translate`.
2. Only then translate **all** cues (70%→95%), streaming each translated batch
   as it lands (`service.go:482-493`).

Because translation cannot start until step 1 finishes for the whole file, the
first translated cue streams minutes into the job, long past the playhead.

### Two distinct bottlenecks (both must be addressed)

- **Transcription stage is global.** Translation waits for the whole-file ASR
  pass. This is the dominant term for a feature film (~20 min ASR) and the
  primary cause. Fixed by interleaving translation per chunk (Phase 1).
- **Extraction is global and precedes any callback.** `Transcribe` calls
  `t.extract(...)` (`transcriber.go:124`) before the `onChunk` loop, and
  `ExtractAudioChunks` blocks on a single whole-track ffmpeg pass
  (`internal/playback/audio_extract.go:34,65`). So even with per-chunk
  interleave and small chunks, the first cue still waits for full-file
  extraction (~1–2 min for a 2h film). Fixed by playhead-first incremental
  extraction (Phase 2).

The transcriber already exposes the per-chunk seam: `Transcribe` invokes
`onChunk(cues, done, total)` per chunk, playhead-first (`transcriber.go:111-174`),
and plain `transcribe` already streams through it (`service.go:426-429`).

Secondary defect found while confirming: `WhisperTranscriber.SetExtraction`
clamps any `chunkSeconds` outside `[60, 600]` to the **default 600**
(`transcriber.go:100-102`), so a small live chunk value silently becomes 10 min
instead of clamping to the floor.

## Goal & honest latency targets

Translated cues stream playhead-first for a session-attached
`transcribe_translate` job, instead of waiting for whole-file transcription.

Time-to-first-translated-cue, by phase:

- **Today:** `extract(full) + transcribe(full) + translate(first batch)` — many
  minutes (≈ full ASR pass).
- **After Phase 1 (interleave, existing full extraction):**
  `extract(full) + transcribe(first chunk) + translate(first chunk)` — removes
  the whole-file ASR pass from the critical path; ≈ extraction time (~1–2 min for
  a 2h film), then cues follow the playhead. Already usable for live after a
  one-time wait.
- **After Phase 2 (playhead-first incremental extraction):**
  `extract(one chunk near playhead) + transcribe + translate` — single-digit
  seconds. This is what the issue means by "within seconds."

Phase 1 delivers most of the value with low risk; Phase 2 closes the remaining
extraction latency. Ship Phase 1 first; do not claim "within seconds" until
Phase 2 lands.

### Non-goals (this plan)

- Pacing/throttling to the playhead (rolling window). Optional in the issue;
  follow-up below.
- Cross-chunk translation-context continuity beyond batch context neighbours.

## Decision: interleave only when streaming

**Session-attached (live) jobs** use the interleaved per-chunk path.
**Background (non-streaming) `transcribe_translate` jobs keep the current
whole-file transcribe-then-translate-at-end path unchanged.** Rationale: the
issue is live-only; this preserves background output byte-for-byte (full
cross-file translation context, no incremental-extraction cost) and keeps the
two behaviors cleanly separated by the existing `streaming` boolean. The
shared transcript-storage step is factored so it is not duplicated.

---

## Phase 1 — interleave translation per chunk (streaming jobs)

### 1a. `internal/subtitles/ai/service.go` — `runTranscribe`

For the chained kind **when `streaming`**, replace the kind-specific `onChunk`
body (`service.go:423-430`) with per-chunk translate-and-stream:

- Capture a translate error in a closure: `var translateErr error` and an
  accumulator `var translatedAccum []SubtitleCue`.
- In `onChunk(chunk, done, total)`:
  - If `translateErr != nil` or `len(chunk) == 0`, return (skip silence; stop
    translating after a prior failure). Do **not** abort transcription.
  - Else call `s.translator.Translate` on **that chunk's** cues. On error: set
    `translateErr`, emit `TranslationFailed` once, and return (let transcription
    continue). On success: stream translated cues via `TranslationCues` and
    append to `translatedAccum`.
  - Progress: single 5%→95% band keyed on chunk `done/total`
    (message "Transcribing & translating").
- Keep `onChunk` signature unchanged (no error return). Aborting on translate
  failure via the callback is intentionally avoided — see 1b.

After `Transcribe` returns:

- **Always store the transcript track** (unchanged, `service.go:447-466`,
  including the transcript `SubtitleReady` broadcast). This must run regardless
  of `translateErr`.
- If `translateErr != nil`: `finishWithError(ctx, job, translateErr)`. The
  transcript is already persisted (cache preserved), and the live session
  already received `TranslationFailed`.
- Else: assemble the translated track from `translatedAccum` (`sortCuesByStart`
  + `StoreSubtitle`, `service.go:499-514`), `CompleteJob`, and
  `TranslationCompleted` (`service.go:515-522`).
- Remove the whole-file translate-at-end (`service.go:479-497`) for the
  streaming path only.

Non-streaming chained jobs fall through to the existing whole-file path,
untouched.

### 1b. Transcript-on-translation-failure (resolves review P1)

Today the full transcript is stored before translation starts
(`service.go:447` then `:482`), so a translation failure still leaves a
transcript/cache row and an already-broadcast `SubtitleReady`. The interleaved
path preserves this exactly because:

- a per-chunk translate failure sets `translateErr` but lets the ASR pass run to
  completion (it does **not** return an error from `onChunk`), so `Transcribe`
  returns the full cue set normally;
- the transcript is then stored on the same post-transcribe path as today,
  before the job is failed for the translated track.

An ASR failure or context cancellation still returns an error from `Transcribe`
and yields no transcript — identical to today.

### 1c. Live progress semantics (resolves review P2)

`SubtitleTranslationCuesPayload.Done/Total` is documented as overall progress
(`internal/playback/realtime.go:172-181`). For live `transcribe_translate`:

- `TranslationCues` carries **`done = chunks completed, total = total chunk
  count`** — the same chunk-granular convention plain `transcribe` already emits
  (`service.go:427-428`). Monotonic and stable across chunks.
- `TranslationStarted.TotalCues = 0` (indeterminate), consistent with the
  transcribe path today (`service.go:412`), since the cue total is unknown before
  ASR completes.
- Cues carry absolute Start/End, so the client places them regardless of
  arrival order; unpause is driven by cue arrival near the playhead, not by
  `Done/Total`. Confirm with client teams that `Done/Total` is treated as a
  fraction, not absolute cue indices (clients already receive chunk counts from
  the plain `transcribe` path, so this should hold).

### 1d. Small live chunks + clamp fix

- Add `TranscribeJobRequest.ChunkSeconds int` (0 = configured default). In
  `Transcribe`, use `req.ChunkSeconds` when `> 0`, else the atomic default
  (`transcriber.go:121-122`); validate/clamp identically to `SetExtraction`.
- Fix the clamp in `SetExtraction` (`transcriber.go:99-105`): clamp
  out-of-range to the nearest bound, not to the 600 default.
- Lower `minASRChunkSeconds` from 60 to 15 (`transcriber.go:26`); update the
  doc comment about request count / boundary word-clip tradeoff.
- Config: add `subtitle_ai.live_asr_chunk_seconds` (default 30):
  - `internal/config/config.go`: new field beside `ASRChunkSeconds`
    (`config.go:274-277`).
  - `internal/config/db_loader.go`: load it next to `asr_chunk_seconds`
    (`db_loader.go:472-476`).
  - `internal/subtitles/ai/config.go`: add `LiveASRChunkSeconds int` to the
    service `Config` so the service can set `req.ChunkSeconds` for streaming jobs.
  - `internal/api/router.go`: plumb through `effectiveSubtitleAIConfig` and the
    `OnConfigChange` `UpdateConfig` (`router.go:1024-1055`).

### 1e. Phase 1 tests

- `service_test.go`: fake `Transcriber` emitting several chunks playhead-first
  via `onChunk`; fake `Translator` recording calls. For a streaming
  `transcribe_translate` job assert: one `Translate` per non-empty chunk; first
  `TranslationCues` streamed before the last chunk is transcribed; empty chunks
  skipped; final stored translated track = all translated cues sorted by start;
  `Done/Total` is chunk-granular and monotonic.
- Failure case: a translate error on chunk N sets the failure but transcription
  completes, the transcript track **is** stored, `TranslationFailed` is emitted,
  and the job ends via `finishWithError`.
- Non-streaming `transcribe_translate`: unchanged whole-file path; correct final
  track; no `TranslationCues`.
- `transcriber_test.go`: `SetExtraction` clamp fix and `ChunkSeconds` override
  (incl. the new 15s floor).

---

## Phase 2 — playhead-first incremental extraction (true seconds latency)

Phase 1 still waits on full-file extraction before the first cue. Phase 2
removes that by extracting and consuming chunks incrementally, seeked to the
playhead.

### 2a. New incremental extractor in `internal/playback`

Add a streaming, seek-based extractor alongside `ExtractAudioChunks`:

```
ExtractAudioChunksFrom(ctx, filePath, audioTrackIndex, dir, ffmpegPath,
    startSec float64, chunkSeconds int,
    onSegment func(AudioChunk) error) error
```

- Runs one ffmpeg pass with `-ss <startSec> -i <file> ... -f segment
  -segment_list segments.csv` covering `startSec`→end.
- Consumes segments **as they complete**: poll the segment-list CSV (ffmpeg
  appends `filename,start,end` when it closes each segment) on a short interval;
  for each new row, invoke `onSegment(AudioChunk{Path, Start: startSec + csvStart})`.
  Reconcile any trailing rows after `cmd.Wait()` so the final segment is not
  missed.
- Honors `ctx` (CommandContext kills ffmpeg on cancel) and `onSegment` errors
  (stop the pass). Caller owns `dir`; segments are deleted by the consumer after
  ASR to cap disk at one extraction.
- Preserves exact per-segment starts from the CSV, so timing does not accumulate
  drift within a pass (only the initial `-ss` seek is packet-accurate, sub-second,
  and is further corrected by the existing `ProbeAudioStartOffset`).

Rejected alternative — **per-chunk seek extraction** (`-ss s -t d` per chunk):
simpler control flow but O(n) ffmpeg spawns and packet-aligned per-chunk seeks
that can clip words at every seam. The single-pass streaming consumer keeps the
existing segment-muxer boundary quality and far fewer process spawns.

### 2b. Transcriber incremental mode

- Add `TranscribeJobRequest.Incremental bool`, set by the service for streaming
  jobs.
- When `Incremental`, `Transcribe` drives playhead-first in two streaming passes
  (reproducing today's `chunkOrderForPosition` order): pass A `startSec =
  playhead → end`, then pass B `0 → playhead`. Each completed segment is fed
  straight into the existing per-chunk ASR loop body (transcribe → `onChunk`),
  which Phase 1 already wires to translate+stream. The full cue set is still
  accumulated and returned for transcript storage.
- When not `Incremental` (background, and plain transcribe unless we opt it in),
  keep the existing whole-file `ExtractAudioChunks` + `chunkOrderForPosition`
  path.

Note: timing composition (`-ss` reset PTS + segment CSV start + probe offset)
must be verified with a fixture; see tests.

### 2c. Phase 2 tests

- `audio_extract` test (or transcriber test with an injected extractor) covering
  incremental emission order, exact-start mapping with a `startSec` offset, and
  ctx-cancel mid-pass.
- Transcriber test asserting `Incremental` yields playhead-first chunk order
  across the two passes and returns the complete cue set for transcript storage.
- Confirm first `onChunk` fires after ~one chunk of audio, not after full-file
  extraction (timing/ordering assertion via a fake extractor, not wall-clock).

---

## Multi-repo / client impact

None expected to the contract. Reuses the existing notifier protocol
(`TranslationStarted` / `TranslationCues` / `TranslationCompleted` /
`TranslationFailed`, `notifier.go`) under the same translated-track `trackKey`.
Translated cues simply begin arriving far earlier, and `Done/Total` keeps the
chunk-granular meaning plain `transcribe` already uses (1c). Worth a smoke test
on one `silo-android` / `silo-apple` client to confirm the live track renders
cues arriving before the transcript track completes, and that `Done/Total` is
treated as a fraction.

## Risks & tradeoffs

- **Translation context at chunk boundaries (live only).** Per-chunk translation
  resets the batch context-neighbour window at each ~30s boundary — accepted
  quality tradeoff for live; background jobs are unchanged.
- **More/smaller ASR requests for live jobs.** 30s chunks mean more requests;
  per-chunk timeout (`chunkSeconds * asrChunkTimeoutFactor`) and per-spawn
  overhead scale; the 15s floor bounds the worst case.
- **Incremental extraction reliability (Phase 2).** Segment-list polling + the
  `-ss` seek path need fixtures for boundary timing, the final-segment race, and
  cancel/cleanup. Single ffmpeg pass per direction keeps cost low.
- **Boundary word-clips.** Smaller live chunks add more fixed-length boundaries
  where a straddling word can clip — already a documented v1 limitation; the
  silence-aligned follow-up would address it.

## Out of scope / follow-ups

- **Pace to playback (issue item 3):** rolling window ahead of the playhead to
  bound work; separate change once interleave + incremental extraction land.
- **Cross-chunk translation context:** feed the previous chunk's tail source
  cues as untranslated context into each chunk's translate call.
- **Silence-aligned chunk boundaries:** reduce boundary word-clips that smaller
  live chunks make more frequent.

## v1 scope note

`docs/architecture/v1-scope.md` is **NOT LOCKED**; the issue carries
`v1-proposed`. This is a **bug fix** to an already-shipped capability (no new
user-facing capability, endpoint, or response field — only an internal config
key and corrected streaming behavior), so it proceeds under the "bug fixes
proceed normally" clause. Link the PR to the AI-subtitle capability item per the
v1 PR requirements.

## Pre-push checklist

- `make lint`
- `cd web && pnpm run lint && pnpm run format:check` (run only if web touched —
  none expected)
- `make verify-local-paths`
- Go tests for `internal/subtitles/ai` and `internal/playback` in a
  libvips-capable container (a bare-host `go test ./...` silently skips CGO
  packages).
