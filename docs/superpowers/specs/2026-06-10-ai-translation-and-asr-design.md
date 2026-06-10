# AI services: shared core, metadata translation, Whisper ASR — design

**Date:** 2026-06-10
**Status:** Draft, pending review
**Scope:** Extract the AI subtitle translation engine into a shared AI core, add AI translation of movie/series/season/episode descriptions into the existing localization tables, and add Whisper ASR subtitle generation (`transcribe`, `transcribe_translate`). One PR.

Commands assume the repository root is the cwd.

## Problem

`internal/subtitles/ai` ships an on-demand LLM subtitle translator. The LLM plumbing inside
it (OpenAI-compatible chat client with retry/backoff, batched indexed-JSON translation,
job lifecycle with idempotency/heartbeat/reaper/bounded concurrency) is generic, but it is
buried in a subtitle-specific package, so nothing else can use it.

Two features want exactly that plumbing:

1. **Metadata translation.** Libraries whose `metadata_language` the providers cannot fully
   serve end up with overviews/taglines in the item's default language (usually English).
   The localization tables (`media_item_localizations`, `season_localizations`,
   `episode_localizations`) and the serving path (`LocalizeItemModel` and friends in
   `internal/catalog/detail.go`) already exist — the gap is producing rows for languages the
   providers don't have.
2. **Whisper ASR.** `subtitle_ai_jobs.kind` already reserves `transcribe` and
   `transcribe_translate`; media with no usable text subtitle track (foreign audio with only
   bitmap subs, or no subs at all) currently has no AI path to a subtitle track.

## Goals

- One shared AI core: a single OpenAI-compatible endpoint configuration (chat + audio
  transcription), one retry/backoff implementation, one generic batched text translator,
  one job-lifecycle runner — consumed by subtitle translation, metadata translation, and ASR.
- AI-translated overviews and taglines stored in the **existing** localization tables, so
  web, Android, Apple, and jellycompat clients get them through the current serving path
  with **zero client changes**.
- Provenance on AI-writable localization fields so refreshes never regress data quality:
  `manual` beats `provider` beats `ai`. A later provider localization overwrites an AI
  translation; the reverse never happens.
- Two metadata-translation triggers: a manual admin action, and an opt-in per-library
  fallback ("provider had no localization for the library language → AI-translate") wired
  into metadata ingestion.
- ASR jobs that produce ordinary `downloaded_subtitles` rows (provider `transcribed`),
  with `transcribe_translate` chaining the existing LLM translator for a target-language
  track; live cue streaming to the requesting playback session, same as translation today.
- Total AI load on the configured endpoint bounded by **one** shared semaphore.

## Non-goals

- **Episode/movie/series title translation.** Titles often have official localized forms
  that are not literal translations; provider data only.
- **Collection names or descriptions.** Deferred: collections have no localization table
  yet. The metadata translation service is written against content-ID + field-list inputs,
  so a `collection_localizations` table can be added in a follow-up without reshaping it.
- **Genre translation.** If ever wanted, that is a static mapping, not an LLM call.
- **Per-profile metadata language.** Presentation language stays library-scoped
  (`media_folders.metadata_language`); changing that is an orthogonal feature.
- **A localization editor UI.** The `manual` provenance value is honored by the write
  rules but no UI sets it yet (the existing metadata editor edits base fields, not
  localizations).
- **Whisper word-level timing, diarization, VAD-aligned chunking.** v1 uses fixed-length
  chunks; boundary-word artifacts are an accepted limitation noted for follow-up.

## Architecture

### Package layout

```
internal/ai/llm        Client: OpenAI-compatible /v1/chat/completions (moved from
                       internal/subtitles/ai/client.go) + new /v1/audio/transcriptions.
                       Shared retry/backoff/429 handling. Config for the endpoint(s).
internal/ai/translate  Generic batched text translation: []Segment{ID, Text} in,
                       translated segments out. Indexed-JSON protocol, context
                       neighbors, malformed-response retries — moved from
                       internal/subtitles/ai/translator.go and de-subtitled.
internal/ai/jobrunner  Generic job lifecycle: bounded dispatch off a shared semaphore,
                       heartbeat loop, stale-job reaper, cancel registry — extracted from
                       internal/subtitles/ai/service.go behind a small repo interface.
internal/subtitles/ai  Stays: subtitle job semantics. Translate path becomes a thin
                       adapter (cues ↔ segments); gains the transcribe paths.
internal/metadata/translation
                       New: metadata translation job service + repo + provenance-aware
                       writes into the localization repos.
```

Package names: `llm`, `aitranslate`, `jobrunner` (import paths above; `aitranslate` avoids
a name collision when imported from package `ai`).

### Shared semaphore

`cmd/silo` builds one `chan struct{}` of size `ai.max_concurrent_jobs` and hands it to both
job services via `jobrunner`. Subtitle, metadata, and ASR jobs all draw from it, so the
operator's endpoint sees a bounded number of concurrent jobs regardless of job mix.
(An ASR job holds one slot for its whole pipeline: extract → transcribe → translate.)

## Settings

Connection settings move to a shared `ai.*` namespace. Because encrypted values are
GCM-bound to their setting key (see `internal/catalog/encrypted_settings_repo.go`), the
legacy rows are **not renamed**; the loader falls back to them, following the precedent of
`recommendations.embedding_auth_token` → `recommendations.openai_api_key` in
`internal/config/db_loader.go`.

| Key | Default | Fallback | Notes |
| --- | --- | --- | --- |
| `ai.base_url` | `https://api.openai.com` | `subtitle_ai.base_url` | |
| `ai.api_key` | `""` | `subtitle_ai.api_key` | sensitive |
| `ai.chat_model` | `gpt-4o-mini` | `subtitle_ai.chat_model` | |
| `ai.max_concurrent_jobs` | `2` | `subtitle_ai.max_concurrent_jobs` | shared semaphore size |
| `ai.asr_model` | `whisper-1` | — | |
| `ai.asr_base_url` | `""` (= `ai.base_url`) | — | separate local Whisper server |
| `ai.asr_api_key` | `""` (= `ai.api_key`) | — | sensitive |
| `subtitle_ai.enabled` | `false` | — | existing; subtitle translation toggle |
| `subtitle_ai.transcribe_enabled` | `false` | — | new; ASR toggle |
| `subtitle_ai.batch_size` | `40` | — | existing; cues per chat request |
| `subtitle_ai.context_neighbors` | `2` | — | existing |
| `metadata_ai.enabled` | `false` | — | new; metadata translation toggle |

`ai.api_key` and `ai.asr_api_key` join `sensitiveSettingKeys`. The admin UI reads through
the same fallback (effective values come from the loaded config) and writes the new keys.

Config structs: `AIConfig` (connection + concurrency), `SubtitleAIConfig` (slims down to
toggles + batch tuning), new `MetadataAIConfig`. Readiness: subtitle translate requires
`subtitle_ai.enabled` + chat config; ASR requires `subtitle_ai.transcribe_enabled` + ASR
config; metadata requires `metadata_ai.enabled` + chat config.

## Metadata translation

### What gets translated

| Target | Fields |
| --- | --- |
| movie / series (`media_item_localizations`) | `overview`, `tagline` |
| season (`season_localizations`) | `overview` |
| episode (`episode_localizations`) | `overview` |

Titles, sort titles, and artwork columns in those rows are never written by AI.

### Provenance

New columns (default `'provider'`, check-constrained to `provider|ai|manual`):

- `media_item_localizations.overview_source`, `media_item_localizations.tagline_source`
- `season_localizations.overview_source`
- `episode_localizations.overview_source`

Write rules, enforced in the localization repos (single-statement upserts with CASE):

| Writer | May overwrite | Sets source | Notes |
| --- | --- | --- | --- |
| provider ingestion | `provider`, `ai` | `provider` | a non-empty provider value always replaces an AI value; an **empty** provider value never blanks an existing one |
| AI translation | `ai`, empty | `ai` | with `force`, may also overwrite `provider` (admin explicitly re-translating) — never `manual` |
| manual (future editor) | anything | `manual` | |

Serving hardening: `LocalizeItemModel` / `LocalizeSeasonModel` / `LocalizeEpisodeModel`
must only override base fields with **non-empty** localization values, since AI-created
rows legitimately carry empty titles. (Verify current behavior; fix if it clobbers.)

### Job model

New table `metadata_translation_jobs`, mirroring `subtitle_ai_jobs` conventions
(status enum, progress, idempotency partial-unique index, heartbeat):

- `target_kind` (`item` | `season` | `episode`), `content_id`, `include_children`
- `target_language`, `source_language` (default language of the base row)
- `engine`, `model`, `status`, `progress`, `progress_message`, `fields_done`, `fields_total`
- `error_message`, `idempotency_key`, `requested_by`, `force`, timestamps, `heartbeat_at`

A job for a series item with `include_children` expands to: item overview + tagline, every
season overview, every episode overview. All fields become segments
(`item:overview`, `season:<content_id>:overview`, `episode:<content_id>:overview`, …) fed
to the generic translator in batches (metadata batch size constant, smaller than the
subtitle one — paragraphs, not cue lines), so episode descriptions share terminology in one
prompt context. The system prompt names the series/movie and year for grounding.

Skip logic in the run loop (not at enqueue): a field whose localization value is already
non-empty is skipped unless `force` — so the auto trigger costs zero LLM calls on repeat
refreshes. If every field is skipped the job completes immediately.

Idempotency key: `content_id | target_kind | target_language | model`, active-only partial
unique index (completed/failed rows never block a retry).

### Triggers

1. **Manual.** Admin action on the item detail metadata editor: pick target language
   (defaults to the library's `metadata_language`), `include_children`, `force`. Gated by
   the same permission as metadata editing.
2. **Auto fallback.** New `media_folders.auto_translate_metadata boolean NOT NULL DEFAULT
   false`. At the end of a metadata refresh for an item (after provider localizations are
   persisted in `internal/metadata/service.go`), if the folder has the flag, has a
   `metadata_language`, that language differs from the item's default metadata language,
   and the localization row for it is missing a translated field — enqueue a non-force job
   for the item (children included for series). Enqueue is fire-and-forget; refresh latency
   is unaffected.

### API

Admin-gated, mirroring the subtitle AI handler shapes:

- `GET  /api/v1/metadata/ai/status`
- `POST /api/v1/metadata/ai/translate` `{content_id, target_kind, target_language, include_children, force}`
- `GET  /api/v1/metadata/ai/jobs/{job_id}` / `POST …/cancel`
- `GET  /api/v1/metadata/ai/jobs?content_id=…`

Progress UI polls the job endpoint (jobs are short; no websocket events in v1).

## Whisper ASR

### Kinds

`subtitle_ai_jobs.kind` gains its reserved values:

- `transcribe` — audio track → subtitle track in the spoken language.
- `transcribe_translate` — `transcribe`, then the existing LLM translation chained to
  `target_language`. Both the transcript track and the translated track are stored (the
  transcript is a cache: a second target language skips ASR entirely because the transcript
  is now a selectable text source for a plain `translate` job). `result_subtitle_id`
  points at the translated track.

For ASR kinds, `source_index` holds the **audio** track index (`-1` = default/first audio
track); `source_language` is an optional hint passed to Whisper, else taken from the
detected `verbose_json.language`.

### Pipeline

1. Resolve the audio track from `media_files`; reject files with no audio.
2. Extract once with ffmpeg into fixed chunks in a job temp dir
   (`-vn -map 0:a:<idx> -ac 1 -ar 16000 -c:a pcm_s16le -f segment -segment_time 600`):
   16 kHz mono WAV, 10-minute chunks (~19 MB, under typical 25 MB API limits). Temp dir
   removed on every exit path.
3. Per chunk: `POST /v1/audio/transcriptions` (multipart: file, model,
   `response_format=verbose_json`, optional `language`, `temperature=0`) via the shared
   `llm.Client` with the shared retry/backoff. Segment timestamps are offset by the chunk
   start and merged.
4. Cue building: one cue per Whisper segment; text wrapped to at most two lines
   (~42 chars/line). Empty/whitespace segments dropped.
5. Store as `downloaded_subtitles` (provider `transcribed`, SRT, release name
   `"<Language> (AI transcribed)"`), notify `SubtitleReady`.
6. `transcribe_translate`: feed the cues to the existing `Translator`; store the translated
   track (provider `translated`) and complete.

Chunks are processed starting from the chunk containing the viewer's playhead, then
wrapping — same UX as translation — and cues stream live to the requesting session through
the existing notifier. Progress bands: extract 0–10 %, transcribe 10–70 % (per chunk),
translate 70–95 %.

Known v1 limitation: fixed chunk boundaries can clip a word at the seam; acceptable, noted
for a silence-aligned follow-up.

### API / UI

The existing enqueue endpoint `POST /api/v1/subtitles/ai/translate` gains an optional
`kind` (default `translate`; backward compatible). `GET /api/v1/subtitles/ai/status` gains
`transcribe_enabled`. The player's translate modal gains a "Generate from audio" mode that
lists audio tracks and a target language — the natural path when the only subtitle tracks
are bitmap (today's hard error) or none exist.

## Frontend

- **AI Services settings page** (`web/src/pages/admin-settings/`): connection card
  (base URL, API key, chat model, ASR model, optional ASR base URL/key), features card
  (subtitle translation, ASR, metadata translation toggles; concurrency; batch tuning).
  The AI card currently inside `SubtitlesSettings.tsx` moves here; subtitle settings keep
  the download-provider config.
- **Metadata editor**: "Translate with AI" action in `EditMetadataDialog.tsx` (language,
  include children, force), with job-poll progress and query invalidation on completion.
- **Library settings**: `auto_translate_metadata` toggle next to the metadata-language
  field.
- **Player**: "Generate from audio" mode in `SubtitleTranslateModal.tsx`.

## Client (Android / Apple) impact

None required. Translated overviews/taglines arrive through the existing localized detail
responses; ASR/translated tracks arrive as ordinary downloaded subtitles. Optional
follow-ups in the client repos: surface "Generate from audio" in their subtitle menus
(the jobs API is shared) and the library auto-translate toggle in their admin surfaces.

## Reliability

- All jobs: idempotency dedup of in-flight work, heartbeat + stale reaper (crash-safe,
  multi-instance safe), terminal-state handling on cancel/shutdown — one implementation in
  `jobrunner`, no copy-paste between the two services.
- Provider-wins/manual-wins provenance is enforced in SQL (single-statement upserts), not
  in racy read-modify-write Go code.
- The shared semaphore bounds total endpoint load; ASR temp space is cleaned on success,
  failure, cancel, and shutdown.
- Auto-translate re-runs are free: skip-if-filled happens before any LLM call.

## Migrations

One Goose migration (`make migrate-create NAME=ai_metadata_translation_and_asr`):

1. `metadata_translation_jobs` table + active-idempotency / content / status indexes.
2. Provenance columns on the three localization tables (+ check constraints).
3. `media_folders.auto_translate_metadata`.

Down: drop the table, columns, and constraint.

## Risks / open questions

- **Whisper-endpoint compatibility.** OpenAI, Groq, faster-whisper-server/speaches all
  speak `verbose_json`, but segment quality varies; the cue builder must tolerate missing
  segment arrays (fall back to one cue per chunk worth of `text` is *not* acceptable —
  fail the job with a clear message instead).
- **`LocalizeItemModel` empty-field semantics** must be verified before AI rows (empty
  titles) ship; this is called out as an explicit plan task.
- **Long-running ASR on slow local Whisper servers** can exceed the 10-minute HTTP client
  timeout per chunk; the transcription call uses a per-request timeout sized for chunk
  duration rather than the client default.
- **Settings sprawl.** The `ai.*`/legacy fallback leaves old `subtitle_ai.*` connection
  rows in place indefinitely; harmless, but a future settings-GC could prune them once the
  new keys are written.
