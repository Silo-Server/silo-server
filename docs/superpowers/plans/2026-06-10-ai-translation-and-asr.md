# AI Services Core, Metadata Translation & Whisper ASR — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Design:** `docs/superpowers/specs/2026-06-10-ai-translation-and-asr-design.md` (read it first; this plan does not repeat the rationale).

**Goal:** Extract the subtitle-AI LLM plumbing into shared packages (`internal/ai/llm`, `internal/ai/translate`, `internal/ai/jobrunner`), add AI translation of overviews/taglines into the existing localization tables with field provenance, and add Whisper ASR subtitle generation (`transcribe`, `transcribe_translate`). Single PR.

**Architecture:** New `internal/ai/*` packages consumed by a refactored `internal/subtitles/ai` and a new `internal/metadata/translation` service. One new Goose migration (jobs table, provenance columns, library auto-translate flag). New `ai.*` settings keys with loader fallback to legacy `subtitle_ai.*` rows (encrypted values are GCM-bound to their key — never rename rows in SQL). Frontend: AI Services settings page, metadata-editor translate action, library toggle, player "generate from audio" mode.

**Tech stack:** Go (chi, pgx), PostgreSQL via Goose SQL migrations, React + TypeScript (TanStack Query), Vitest.

Commands assume the repository root is the cwd.

---

## Ordering and verification

Tasks 1–4 are a pure refactor and must leave subtitle translation behaviorally unchanged — run `go build ./... && go test ./internal/...` after each. Tasks 5–11 are backend features; 12–15 frontend; 16 final verification.

Before opening the MR: `make lint`, `cd web && pnpm run lint && pnpm run format:check`, `make verify-local-paths`.

---

## File structure

**Create**
- `internal/ai/llm/client.go`, `client_test.go`, `config.go` — shared OpenAI-compatible client (chat + transcription)
- `internal/ai/llm/transcribe.go` — multipart `/v1/audio/transcriptions`, `verbose_json` types
- `internal/ai/translate/translate.go`, `translate_test.go` — generic segment batch translator (package `aitranslate`)
- `internal/ai/jobrunner/runner.go`, `runner_test.go` — dispatch/semaphore/heartbeat/reaper/cancel registry
- `internal/metadata/translation/{job.go,pgrepo.go,service.go,prompt.go,service_test.go}`
- `internal/subtitles/ai/transcriber.go`, `transcriber_test.go` — ASR pipeline
- `internal/playback/audio_extract.go` — ffmpeg chunked audio extraction helper
- `internal/api/handlers/metadata_ai.go`
- `migrations/sql/<timestamp>_ai_metadata_translation_and_asr.sql` (via `make migrate-create`)
- `web/src/pages/admin-settings/AIServicesSettings.tsx`

**Modify**
- `internal/subtitles/ai/{client.go→deleted,translator.go,service.go,engine.go,job.go,config.go,pgrepo.go}`
- `internal/config/{config.go,db_loader.go}`; `internal/catalog/encrypted_settings_repo.go`
- `internal/catalog/{localization_repo.go,detail.go}`; `internal/models/media.go`
- `internal/metadata/service.go` (provider provenance + auto-enqueue hook)
- `internal/api/handlers/subtitle_ai.go`; router registration; `cmd/silo/main.go` wiring
- `web/src/pages/admin-settings/SubtitlesSettings.tsx`, `web/src/components/EditMetadataDialog.tsx`, `web/src/player/components/SubtitleTranslateModal.tsx`, library settings form, `web/src/api/` types/client, admin settings nav/route registration

---

## Task 1: `internal/ai/llm` — shared client

- [ ] **Step 1: Move the chat client.** Create `internal/ai/llm` (package `llm`). Move `Client`, `chat` (export as `Chat`), `chatMessage` (export as `Message`), retry helpers (`sleepCtx`, `rateLimitBackoff`, `truncate`) from `internal/subtitles/ai/client.go`. New `llm.Config`:

```go
type Config struct {
    BaseURL    string // chat endpoint, no trailing /v1
    APIKey     string
    ChatModel  string
    ASRBaseURL string // empty = BaseURL
    ASRAPIKey  string // empty = APIKey
    ASRModel   string
    MaxConcurrentJobs int
}
```

Log lines lose the "subtitle" wording (they are shared now).

- [ ] **Step 2: Add `Transcribe`.** In `transcribe.go`:

```go
type TranscribeRequest struct {
    Filename string
    Audio    io.Reader
    Language string        // optional ISO-639-1 hint
    Timeout  time.Duration // per-request; sized to chunk length by the caller
}
type TranscriptionSegment struct{ Start, End float64; Text string }
type Transcription struct{ Language string; Segments []TranscriptionSegment }
func (c *Client) Transcribe(ctx context.Context, req TranscribeRequest) (*Transcription, error)
```

Multipart fields: `file`, `model` (=`ASRModel`), `response_format=verbose_json`, `temperature=0`, optional `language`. Reuse the same 429/5xx/transport retry-with-backoff loop as `Chat` (extract a shared `doWithRetry` rather than duplicating it). Empty `segments` in the response is an error (`transcription returned no segments`), not a silent fallback.

- [ ] **Step 3: Tests** (`client_test.go`, httptest): chat retries on 429 (honors `Retry-After`), 5xx, 200-with-error-object, empty choices; transcribe happy path (multipart fields present, segments parsed), missing-segments error, ASR base-url/key override falling back to chat values.

- [ ] **Step 4: Commit.** `refactor(ai): extract shared OpenAI-compatible LLM client with transcription support`

---

## Task 2: `internal/ai/translate` — generic segment translator

- [ ] **Step 1: Move the batch logic.** Package `aitranslate`. Move `buildIndexedJSON`, `extractJSONObject`, and the batch loop from `internal/subtitles/ai/translator.go`, generalized:

```go
type Segment struct{ ID, Text string }
type Request struct {
    Segments         []Segment
    SystemPrompt     string // caller-supplied; domain-specific
    BatchSize        int
    ContextNeighbors int    // preceding source segments sent untranslated
}
type ChatFn func(ctx context.Context, system, user string) (string, error)
func Translate(ctx context.Context, chat ChatFn, req Request,
    onBatch func(batch []Segment, done, total int)) ([]Segment, error)
```

Wire protocol unchanged: 1-based indexed JSON per batch, same-keys response, malformed-response retries (`maxRetries = 2`), completeness check. `ChatFn` keeps the package free of an `llm` dependency and trivially testable.

- [ ] **Step 2: Tests:** batch splitting/boundaries, context neighbors, code-fence tolerance, omitted-key retry then failure, ctx cancellation between batches.

- [ ] **Step 3: Commit.** `refactor(ai): extract generic batched segment translator`

---

## Task 3: `internal/ai/jobrunner` — shared job lifecycle

- [ ] **Step 1: Extract the runner.** Move dispatch/heartbeat/reaper/cancel mechanics from `internal/subtitles/ai/service.go` (lines ~136–319) behind:

```go
type Store interface {
    Heartbeat(ctx context.Context, id int64) error
    ResetStaleJobs(ctx context.Context, before time.Time, message string) (int64, error)
    MarkCancelled(ctx context.Context, id int64, message string) error
}
type Runner struct{ /* baseCtx, sem (shared), store, logger, cancels, wg */ }
func New(appCtx context.Context, sem chan struct{}, store Store, logger *slog.Logger) *Runner
func (r *Runner) Recover()                      // reap + background reaper loop
func (r *Runner) Dispatch(id int64, run func(ctx context.Context)) // semaphore + heartbeat + cancel registry
func (r *Runner) Cancel(id int64) bool          // true if an in-flight goroutine was cancelled
```

Heartbeat-while-queued behavior (the comment block in `dispatch`) must be preserved verbatim — it is load-bearing for multi-instance safety. Constants (30 s heartbeat, 2 min stale, 1 min reaper) move here.

- [ ] **Step 2: Shared semaphore.** The semaphore is **constructed by the caller** (`cmd/silo`) at size `ai.max_concurrent_jobs` and passed to every `Runner`, so subtitle + metadata + ASR jobs share one bound.

- [ ] **Step 3: Tests:** dispatch bounded by a size-1 shared semaphore across two runners; cancel of queued job marks cancelled without running; reaper resets a stale heartbeat row (fake store).

- [ ] **Step 4: Commit.** `refactor(ai): extract shared job lifecycle runner`

---

## Task 4: Refactor `internal/subtitles/ai` onto the shared core

- [ ] **Step 1:** Delete `client.go`; `LLMTranslator` becomes an adapter: cues → `aitranslate.Segment` (ID = 1-based index, Text = joined lines), subtitle system prompt stays here, `splitCueLines` maps back. `Service` keeps job semantics but delegates lifecycle to `jobrunner.Runner`; `pgrepo` satisfies `jobrunner.Store`.
- [ ] **Step 2:** Update `cmd/silo/main.go` wiring (build `llm.Client` + shared semaphore once; pass into the subtitle service).
- [ ] **Step 3:** `go build ./... && go test ./internal/...` — subtitle translation behavior unchanged (same prompts, same batching, same job rows).
- [ ] **Step 4: Commit.** `refactor(subtitles): consume shared AI core`

---

## Task 5: Migration

- [ ] **Step 1:** `make migrate-create NAME=ai_metadata_translation_and_asr`, then fill the generated file:

```sql
-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.metadata_translation_jobs (
    id               bigserial   PRIMARY KEY,
    target_kind      text        NOT NULL,                   -- 'item' | 'season' | 'episode'
    content_id       text        NOT NULL,
    include_children boolean     NOT NULL DEFAULT true,
    source_language  text        NOT NULL DEFAULT '',
    target_language  text        NOT NULL,
    engine           text        NOT NULL DEFAULT 'openai',
    model            text        NOT NULL DEFAULT '',
    status           text        NOT NULL DEFAULT 'pending', -- pending|running|completed|failed|cancelled
    progress         double precision NOT NULL DEFAULT 0,
    progress_message text        NOT NULL DEFAULT '',
    fields_done      integer     NOT NULL DEFAULT 0,
    fields_total     integer     NOT NULL DEFAULT 0,
    force            boolean     NOT NULL DEFAULT false,
    error_message    text        NOT NULL DEFAULT '',
    idempotency_key  text        NOT NULL,
    requested_by     integer,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    heartbeat_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX metadata_translation_jobs_active_idempotency_idx
    ON public.metadata_translation_jobs (idempotency_key)
    WHERE status IN ('pending', 'running');
CREATE INDEX metadata_translation_jobs_content_idx
    ON public.metadata_translation_jobs (content_id, created_at DESC);
CREATE INDEX metadata_translation_jobs_status_idx
    ON public.metadata_translation_jobs (status) WHERE status IN ('pending', 'running');

-- Field provenance: 'provider' | 'ai' | 'manual'. manual > provider > ai.
ALTER TABLE media_item_localizations
    ADD COLUMN overview_source text NOT NULL DEFAULT 'provider'
        CHECK (overview_source IN ('provider', 'ai', 'manual')),
    ADD COLUMN tagline_source  text NOT NULL DEFAULT 'provider'
        CHECK (tagline_source IN ('provider', 'ai', 'manual'));
ALTER TABLE season_localizations
    ADD COLUMN overview_source text NOT NULL DEFAULT 'provider'
        CHECK (overview_source IN ('provider', 'ai', 'manual'));
ALTER TABLE episode_localizations
    ADD COLUMN overview_source text NOT NULL DEFAULT 'provider'
        CHECK (overview_source IN ('provider', 'ai', 'manual'));

ALTER TABLE media_folders
    ADD COLUMN auto_translate_metadata boolean NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE media_folders DROP COLUMN auto_translate_metadata;
ALTER TABLE episode_localizations DROP COLUMN overview_source;
ALTER TABLE season_localizations DROP COLUMN overview_source;
ALTER TABLE media_item_localizations DROP COLUMN tagline_source, DROP COLUMN overview_source;
DROP TABLE IF EXISTS public.metadata_translation_jobs;
-- +goose StatementEnd
```

- [ ] **Step 2:** `make migrate-up` against the local stack (`docker compose up -d postgres redis`), then `make migrate-status`.
- [ ] **Step 3: Commit.** `feat(metadata): migration for translation jobs, localization provenance, library auto-translate`

---

## Task 6: Settings & config

- [ ] **Step 1: Config structs** (`internal/config/config.go`): add `AIConfig` (fields mirroring `llm.Config`); slim `SubtitleAIConfig` to `Enabled`, `TranscribeEnabled`, `BatchSize`, `ContextNeighbors`; add `MetadataAIConfig{Enabled bool}`.
- [ ] **Step 2: Loader** (`internal/config/db_loader.go`): load `ai.*` with legacy fallback, following the `recommendations.embedding_auth_token` precedent at line ~409:

```go
cfg.AI.BaseURL  = stringOr(m, "ai.base_url",  stringOr(m, "subtitle_ai.base_url", "https://api.openai.com"))
cfg.AI.APIKey   = stringOr(m, "ai.api_key",   stringOr(m, "subtitle_ai.api_key", ""))
cfg.AI.ChatModel = stringOr(m, "ai.chat_model", stringOr(m, "subtitle_ai.chat_model", "gpt-4o-mini"))
// ai.max_concurrent_jobs ← subtitle_ai.max_concurrent_jobs ← 2
// ai.asr_model (default "whisper-1"), ai.asr_base_url, ai.asr_api_key (default "")
// subtitle_ai.transcribe_enabled, metadata_ai.enabled (default false)
```

**Never** rename the legacy rows in SQL — encrypted values are GCM-bound to their setting key (`internal/catalog/encrypted_settings_repo.go`).

- [ ] **Step 3:** Add `ai.api_key`, `ai.asr_api_key` to `sensitiveSettingKeys`.
- [ ] **Step 4:** Readiness helpers: subtitle translate = `SubtitleAI.Enabled && AI chat ready`; ASR = `SubtitleAI.TranscribeEnabled && AI.ASRModel != "" && (asr or chat base URL set)`; metadata = `MetadataAI.Enabled && AI chat ready`.
- [ ] **Step 5: Commit.** `feat(config): shared ai.* settings with legacy subtitle_ai fallback`

---

## Task 7: Provenance-aware localization writes

- [ ] **Step 1: Models** (`internal/models/media.go`): add `OverviewSource`/`TaglineSource` to `MediaItemLocalization`, `OverviewSource` to `SeasonLocalization`/`EpisodeLocalization`.
- [ ] **Step 2: Provider upsert rules** (`internal/catalog/localization_repo.go`): rewrite the three `Upsert` statements so, per AI-writable field:
  - existing source `manual` → keep existing value and source;
  - incoming value empty → keep existing value and source;
  - otherwise → take incoming value, set source `provider`.

Single-statement `ON CONFLICT … DO UPDATE` with `CASE` (no read-modify-write). Pattern for one field:

```sql
overview = CASE
    WHEN media_item_localizations.overview_source = 'manual' THEN media_item_localizations.overview
    WHEN EXCLUDED.overview = '' THEN media_item_localizations.overview
    ELSE EXCLUDED.overview END,
overview_source = CASE
    WHEN media_item_localizations.overview_source = 'manual' THEN media_item_localizations.overview_source
    WHEN EXCLUDED.overview = '' THEN media_item_localizations.overview_source
    ELSE 'provider' END
```

- [ ] **Step 3: AI upsert methods**: `UpsertAITranslation(ctx, contentID, language string, overview, tagline *string, force bool)` per repo (nil pointer = field not part of this write). Insert path populates only the translated fields (other text columns empty). Update path per field: write when existing source is `ai`, existing value is empty, or `force` — never when `manual`; `force` may overwrite `provider` (the admin asked). Sets source `ai`.
- [ ] **Step 4: Serving hardening** (`internal/catalog/detail.go`): verify `LocalizeItemModel` / `LocalizeSeasonModel` / `LocalizeEpisodeModel` only override base fields with **non-empty** localization values (AI rows carry empty titles). Fix any field that clobbers; add a regression test.
- [ ] **Step 5: Tests** for the upsert matrix (provider-over-ai, ai-skips-provider-unless-force, manual-untouchable, empty-never-blanks), following the existing repo/service test patterns in `internal/catalog`.
- [ ] **Step 6: Commit.** `feat(catalog): field provenance for localizations with provider/ai/manual precedence`

---

## Task 8: `internal/metadata/translation` service

- [ ] **Step 1: Job model + repo** (`job.go`, `pgrepo.go`): mirror `internal/subtitles/ai/{job.go,pgrepo.go}` shapes against `metadata_translation_jobs`; satisfy `jobrunner.Store`. Idempotency key = SHA-256 of `content_id|target_kind|target_language|model`.
- [ ] **Step 2: Service** (`service.go`): `Enqueue`, `GetJob`, `ListJobs(contentID)`, `Cancel`, `Recover`, using `jobrunner.Runner` + the shared semaphore. The run loop:
  1. Expand targets: `item` → item overview+tagline; series item with `include_children` → + every season overview + every episode overview; `season` (+children → its episodes); `episode` → its overview. Source text comes from the **base** rows (default metadata language); `source_language` recorded from the item's `default_metadata_language`.
  2. Skip-if-filled: drop any field whose target-language localization value is already non-empty, unless `force`. All skipped → complete immediately ("Nothing to translate").
  3. Build `aitranslate.Segment`s (IDs `item:overview`, `item:tagline`, `season:<content_id>:overview`, `episode:<content_id>:overview`), translate with `prompt.go`'s system prompt (names the title/year, "translate media catalog descriptions", preserve proper nouns/character names/tone, no added information), `BatchSize` = package constant `metadataBatchSize = 10`, `ContextNeighbors = 0`.
  4. Per batch: provenance-aware AI upserts (Task 7) + `fields_done`/progress update. Persisting per batch means a cancelled job keeps completed fields.
- [ ] **Step 3: Tests** (fake repos + fake `ChatFn`): series expansion counts, skip-if-filled short-circuit (zero chat calls), force overwrites `ai`+`provider` but never `manual`, per-batch persistence on cancellation mid-job.
- [ ] **Step 4: Commit.** `feat(metadata): AI translation service for overviews and taglines`

---

## Task 9: Ingestion hook + library flag

- [ ] **Step 1:** Switch the provider localization writes in `internal/metadata/service.go` (item ~line 1476, season ~2921, episode ~3080) to the provenance-aware upserts — no other behavior change.
- [ ] **Step 2:** Thread `auto_translate_metadata` through the `MediaFolder` model, folder repo scan/update, and the folder settings API payload.
- [ ] **Step 3: Auto-enqueue hook.** After an item's refresh persists (single point at the end of the item flow — not per season/episode), when: folder flag set, folder `metadata_language` non-empty and ≠ item `default_metadata_language`, metadata AI ready, and the item's target-language localization is missing overview (or tagline) → `Enqueue` a non-force `item` job with `include_children=true`. Fire-and-forget (log on error); the active-idempotency index plus skip-if-filled make repeat refreshes free.
- [ ] **Step 4: Commit.** `feat(metadata): per-library auto-translate fallback on refresh`

---

## Task 10: Metadata AI API

- [ ] **Step 1:** `internal/api/handlers/metadata_ai.go`, mirroring `subtitle_ai.go` shapes:
  - `GET  /api/v1/metadata/ai/status` → `{enabled}`
  - `POST /api/v1/metadata/ai/translate` `{content_id, target_kind, target_language, include_children, force}` → `{job}` (joins an in-flight duplicate, same as subtitles)
  - `GET  /api/v1/metadata/ai/jobs/{job_id}`, `POST /api/v1/metadata/ai/jobs/{job_id}/cancel`, `GET /api/v1/metadata/ai/jobs?content_id=…`
- [ ] **Step 2:** Gate with the same permission as metadata editing (metadata curation permission; see `internal/catalog/update.go` callers). Register routes; wire the service in `cmd/silo/main.go`.
- [ ] **Step 3:** Handler tests following `internal/api/handlers` conventions (validation: bad kind, missing language, disabled engine → 400/503 mapping).
- [ ] **Step 4: Commit.** `feat(api): metadata AI translation endpoints`

---

## Task 11: Whisper ASR

- [ ] **Step 1: Audio extraction** (`internal/playback/audio_extract.go`), alongside the existing subtitle extraction helpers:

```go
// ExtractAudioChunks extracts one audio track to 16 kHz mono WAV chunks in dir.
// Returns ordered chunk paths; chunkSeconds = 600 in production.
func ExtractAudioChunks(ctx context.Context, filePath string, audioTrackIndex int,
    dir, ffmpegPath string, chunkSeconds int) ([]string, error)
```

Single ffmpeg pass: `-vn -map 0:a:<idx> -ac 1 -ar 16000 -c:a pcm_s16le -f segment -segment_time <n> <dir>/chunk%05d.wav`.

- [ ] **Step 2: Transcriber** (`internal/subtitles/ai/transcriber.go`):

```go
type Transcriber interface {
    Transcribe(ctx context.Context, req TranscribeJobRequest,
        onChunk func(cues []SubtitleCue, done, total int)) ([]SubtitleCue, string /*detected lang*/, error)
}
```

`WhisperTranscriber` implementation: temp dir (`os.MkdirTemp`, removed on all exit paths) → `ExtractAudioChunks` → per chunk `llm.Client.Transcribe` (per-request timeout ∝ chunk duration; pass the job's language hint) → offset segment times by `chunkIndex*chunkSeconds` → cues (one per segment, wrapped to ≤2 lines ×~42 chars via a `wrapCueText` helper; whitespace-only segments dropped). Chunk processing order starts at the chunk containing `StartPosition`, then wraps (mirrors `reorderFromPosition` semantics).

- [ ] **Step 3: Wire kinds into the service.** `Service.run` branches on `job.Kind`:
  - `translate`: existing path.
  - `transcribe`: resolve audio track (`source_index` = audio index, `-1` → default/first; reject files without audio), transcribe (progress 10–70 %, live cue streaming via the existing notifier callbacks), store SRT as provider `transcribed`, language = hint or detected, release name `"<Language> (AI transcribed)"`, notify `SubtitleReady`.
  - `transcribe_translate`: `transcribe`, store the transcript track, then run the existing `Translator` on the cues (70–95 %), store provider `translated`, `result_subtitle_id` = translated track.

Enqueue validation per kind (ASR requires `TranscribeEnabled`; idempotency model component = `asr_model` or `asr_model+chat_model` for the chained kind).

- [ ] **Step 4:** API surface: `kind` field (optional, default `translate`) on the existing enqueue request in `subtitle_ai.go`; `transcribe_enabled` on the status response.
- [ ] **Step 5: Tests** (`transcriber_test.go`, fake client): timestamp stitching across chunks, playhead-first chunk ordering, cue wrapping, empty-segments chunk → job error, temp-dir cleanup on failure (assert via `t.TempDir` layout).
- [ ] **Step 6: Commit.** `feat(subtitles): Whisper ASR transcribe and transcribe_translate jobs`

---

## Task 12: Frontend — AI Services settings page

- [ ] **Step 1:** `web/src/pages/admin-settings/AIServicesSettings.tsx`: connection card (`ai.base_url`, `ai.api_key` via the sensitive-key pattern already used in `SubtitlesSettings.tsx:264`, `ai.chat_model`, `ai.asr_model`, optional `ai.asr_base_url`/`ai.asr_api_key`, `ai.max_concurrent_jobs`) + features card (`subtitle_ai.enabled`, `subtitle_ai.transcribe_enabled`, `metadata_ai.enabled`, `subtitle_ai.batch_size`, `subtitle_ai.context_neighbors`). Reads show effective values (new key, falling back to the legacy key when unset — same fallback order as the loader); writes always target the new `ai.*` keys.
- [ ] **Step 2:** Remove the AI card from `SubtitlesSettings.tsx` (leave a link/hint to the new page); register the page in the admin settings nav + route following the existing page registrations.
- [ ] **Step 3: Commit.** `feat(web): AI services settings page`

---

## Task 13: Frontend — metadata translate action

- [ ] **Step 1:** API client functions + types for the Task 10 endpoints.
- [ ] **Step 2:** `EditMetadataDialog.tsx`: "Translate with AI" action (visible when `metadata_ai` status is enabled): target language (default = library `metadata_language`), `include_children` (series only), `force` checkbox; enqueue, poll the job every ~1.5 s, show `progress_message`/`fields_done`, invalidate the item detail query on completion.
- [ ] **Step 3: Commit.** `feat(web): translate descriptions from the metadata editor`

---

## Task 14: Frontend — library auto-translate toggle

- [ ] **Step 1:** Add the `auto_translate_metadata` switch to the library settings form next to the metadata-language field (helper text: "When metadata providers have no translation for this library's language, translate descriptions with AI"). Disabled state with hint when `metadata_ai` is off.
- [ ] **Step 2: Commit.** `feat(web): library auto-translate toggle`

---

## Task 15: Frontend — player "Generate from audio"

- [ ] **Step 1:** `SubtitleTranslateModal.tsx`: when `transcribe_enabled`, add a source option "Generate from audio (AI)" listing the file's audio tracks; submit with `kind: "transcribe"` (or `transcribe_translate` when the chosen output language differs from the audio language). Surface it prominently when no text subtitle source exists (today's dead end for bitmap-only files).
- [ ] **Step 2: Commit.** `feat(web): generate subtitles from audio in the player`

---

## Task 16: Verification

- [ ] `go build ./... && go test ./...`
- [ ] `make lint`
- [ ] `cd web && pnpm run lint && pnpm run format:check`
- [ ] `make verify-local-paths`
- [ ] Manual smoke against the local stack: enqueue a metadata translation for a series (verify localization rows + provenance values), re-run a refresh (verify provider values overwrite `ai` rows and `manual` simulation survives), run a `transcribe` job on a short file against a local Whisper-compatible server.
- [ ] MR description: problem, approach, link to the design doc, risks (Whisper endpoint variance, chunk-boundary artifacts), AI-use disclosure, screenshots of the new settings page / editor action / player mode.
