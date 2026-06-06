# Marker sources & TheIntroDB contribution — design

**Date:** 2026-06-06
**Status:** Draft (rev 2), pending review
**Scope:** Review the current TheIntroDB marker integration, formalize a multi-source
marker-provider model (query-all / best-wins), and add the ability to contribute Silo's own
markers back to TheIntroDB. This round designs the **server-side API and data model only** —
web-admin and mobile surfaces are deferred to a UI-only follow-up. No client (Android/Apple)
changes required.

Commands assume the repository root is the cwd.

**Rev 2 changes:** contribution is configured **per provider** (off by default); all new web
UI is **deferred** and the backend API is specified in full instead.

## Decisions locked for this design

These were chosen up front and constrain everything below:

1. **Deliverable now:** design/plan only. No source changes land from this document; it is
   the blueprint a follow-up implementation plan expands.
2. **What we contribute:** *both* local auto-detected markers *and* manual corrections. Local
   detection only produces **intro markers for episodes**, so credits/recap/preview and all
   movies can only be contributed via manual correction — this asymmetry drives the design.
3. **Account model:** a single server-wide TheIntroDB account, reusing the existing
   `introdb.api_key` server setting. Per-user accounts are a noted future seam, not built now.
4. **Multi-source dispatch:** query all enabled online sources and keep the highest-quality
   result per segment ("best wins"). This makes *real per-segment confidence* (gap #2 below)
   a hard prerequisite, not an optional nicety.
5. **Contribution is configured per provider, off by default.** Every contribution-capable
   provider has its own enable switch and submit settings, so when more providers exist an
   operator chooses *which* ones to submit to. Both the master contribute switch and the
   auto-submit-local-detections switch default to off; a single setting flips each on.
6. **API-first, UI deferred.** This round fully specifies the backend (manual marker
   read/write/clear, contribution trigger + status, per-provider provider config, key
   validation and stats) so any future surface — web admin or mobile — builds on a complete
   contract. No new web UI is designed here beyond noting where it will eventually attach.

## Problem

Silo currently detects intro/recap/credits/preview markers from two places — a local
chromaprint/chapter analyzer (`internal/intromarkers`) and the online TheIntroDB provider
(`internal/markers/introdb`) — reconciled by a source-priority ladder at write time. The
online side is cleanly abstracted behind a `markers.Provider` interface and a
`markers.Registry`, which is good, but three things are missing:

1. The current TheIntroDB provider has **correctness gaps** measured against the real v3 API
   contract (TVDB lookups dropped, real confidence discarded — see "Review findings").
2. There is **no path to contribute** Silo's markers back to TheIntroDB, even though the v3
   API exposes a submission endpoint and the package was deliberately built read-only.
3. The provider abstraction is **fetch-only and first-hit**, so it does not yet support
   "several online sources, keep the best answer" or "this source can also accept
   contributions, and I choose which sources to submit to."

## Review findings — current implementation

### What is already right (keep it)

- **Clean source abstraction.** `markers.Provider` (`ID()`, `FetchMarkers()`) + `markers.Registry`
  with `FetchFirstHit` (`internal/markers/types.go`). TheIntroDB is *not* hardcoded; a second
  provider is one `Register()` call (`cmd/silo/main.go`).
- **Multi-source-aware schema.** `media_files` carries per-segment provenance for all four
  kinds: `{intro,credits,recap,preview}_markers_{source,provider,confidence,algorithm,detected_at}`
  (migrations 108/109/137).
- **Write-time reconciliation across source classes.** `CanWriteMarker`
  (`internal/markers/write.go`) + `MarkerSourcePriority` (`internal/models/marker_source.go`):
  `manual(4) > online/plugin(3) > s3(2) > scanner(1)`, confidence breaks ties. Local detection
  uses its own priority-gated path (`intromarkers.Repository.PatchIntroMarker`).
- **Solid HTTP hygiene** in `internal/markers/introdb/client.go`: conservative rate limiter
  (2 req/s, burst 5; documented limit is 30 req/10s), retry with exponential backoff,
  `Retry-After` handling, 24h TTL cache with negative caching, hot-reloadable API key over
  Redis pub/sub.

### Gaps (measured against TheIntroDB's own v3 client + Jellyfin plugin)

| # | Gap | Evidence | Impact |
|---|-----|----------|--------|
| 1 | **TVDB lookups silently dropped.** Provider reads only TMDB+IMDB; client can't send `tvdb_id`. | `internal/markers/introdb/provider.go` (reads `ExternalIDKeyTMDB`/`IMDB` only); `client.go` `FetchEpisode/FetchMovie` build `tmdb_id` else `imdb_id`. | Titles matched only by TVDB (very common for anime / TheTVDB-first libraries) get **zero** markers, even though the resolver already populates `TvdbID` and the v3 API + both official clients support `tvdb_id`. Correctness bug. |
| 2 | **Real confidence + submission_count discarded; hardcoded 0.9.** | `internal/markers/introdb/types.go` `segmentTimestamps` has only `start_ms`/`end_ms`; `pickMarker` sets `Confidence: 0.9`. | The `/media` response carries per-segment `confidence` and `submission_count`. Without them, "best wins" merging can't rank sources, and write-time confidence tie-breaks are meaningless. Hard blocker for the chosen dispatch model. |
| 3 | **Naive multi-candidate selection.** `pickMarker` takes the *first* usable entry. | `internal/markers/introdb/provider.go`. | When several candidates remain (e.g. no duration match), first-usable may not be the most-submitted/highest-confidence. Minor today (duration narrows the set), but compounds once we rank by confidence. |
| 4 | **No contribution path** (by design). | `internal/markers/introdb/types.go` package doc: "Submissions are intentionally not supported." | The whole point of this effort. |
| 5 | **Provider is fetch-only.** No submit capability in the interface. | `markers.Provider` in `internal/markers/types.go`. | Contribution needs an optional `Submitter` capability so non-contributing sources don't have to implement it, and so submission is per provider. |
| 6 | **No key validation / contribution stats.** | `IntroDBCredentialCard` in `web/src/pages/admin-settings/IntegrationsSettings.tsx` only sets the key. | v3 exposes `GET /v3/user/stats` to validate a key and show contribution totals/streaks. Useful "connect & verify" probe, currently absent from the API. |
| 7 | **Single collapsed confidence in the write payload.** `BuildUpdatePayload` promotes one max confidence across all segments. | `internal/markers/write.go`. | The DB has *per-segment* confidence columns, but a merged result (intro from source A, credits from source B) can't populate them independently until per-segment confidence is threaded through the payload. Coupled to gap #2. |

## Authoritative API contract (TheIntroDB v3)

Confirmed from TheIntroDB's own `theintrodb` npm client (`src/funcs.ts`, `src/types.ts`) and
the official Jellyfin plugin (`TheIntroDbClient.cs`). Base URL: `https://api.theintrodb.org/v3`.

### `GET /media` — read (public; optional key)

Query: one of `tmdb_id` / `tvdb_id` / `imdb_id` (preference order tmdb → tvdb → imdb),
plus `season` + `episode` together for TV, plus optional `duration_ms` (selects the closest
release version). Optional `Authorization: Bearer <key>` additionally includes that user's
*pending* submissions.

Response — each segment kind is an array of:
```jsonc
{ "start_ms": 30000|null, "end_ms": 90000|null, "confidence": 0.93|null, "submission_count": 7|null }
```
`start_ms:null` = "from the beginning" (intro/recap); `end_ms:null` = "to the end"
(credits/preview). Arrays may hold multiple entries (multiple release versions) — pick by
duration match, then by `submission_count`/`confidence`.

### `POST /submit` — contribute (requires key)

`Authorization: Bearer <user key>` required; submissions are credited to that account.
Body (one segment per call):
```jsonc
{
  "tmdb_id": 1234,            // required
  "imdb_id": "tt0903747",     // optional
  "type": "tv" | "movie",
  "segment": "intro" | "recap" | "credits" | "preview",
  "season": 1,                // TV only (number, or "1,2,3" multi-select)
  "episode": 1,               // TV only
  "video_duration_ms": 2760000, // optional, strongly recommended (release-version match)
  "start_ms": 0|null,         // intro/recap may be null (= beginning)
  "end_ms": 90000|null        // credits/preview may be null (= end)
}
```
Response: `{ "submissions": [ { "id": uuid, "status": "pending"|"accepted"|"rejected",
"weight": number, ... } ] }`. Submissions enter as `pending`; the backend weight-averages
timestamps across submissions and moderates to accepted/rejected.

Validation rules to mirror client-side: intro/recap require an **end**; credits/preview
require a **start**; TV requires season+episode; movies omit them; max timestamp 6h
(21,600,000 ms); `video_duration_ms` is 0 (unknown) or ≥ 300,000 ms.

### `GET /user/stats` — validate key + contribution stats

`Authorization: Bearer <key>`. Returns `{ total, accepted, pending, rejected,
acceptance_rate, current_streak, best_streak, total_time_saved_ms, top_media }`. A 2xx with no
`error` means the key is valid. This is both our key-validation probe and the stats display.

### Rate / usage limits

429 responses carry `X-RateLimit-{Limit,Remaining,Reset}` and `X-UsageLimit-{Limit,Remaining,Reset}`.
Reads count against the rate limit; **submissions count against the usage limit**. The
contribution pipeline must honor `X-UsageLimit-Reset` and back off — reuse the existing
limiter and add usage-limit awareness.

## Target architecture

Three layers, each independently shippable. Layer A is pure correctness and unblocks B and C.

### Layer A — Correctness foundation (gaps #1, #2, #3, #7)

- **TVDB in the client + provider.** Add `tvdb_id` to the query builder in
  `internal/markers/introdb/client.go` (`FetchEpisode`/`FetchMovie` gain a `tvdbID` arg,
  preference tmdb → tvdb → imdb, cache keys extended). Provider reads
  `req.ExternalIDs[markers.ExternalIDKeyTVDB]`. Resolver already supplies it — no resolver
  change.
- **Capture confidence + submission_count.** Extend `segmentTimestamps` with
  `Confidence *float64` and `SubmissionCount *int`; add `SubmissionCount int` to
  `markers.Marker`; `pickMarker` uses the real confidence (fallback to a named default
  constant, e.g. `0.9`, only when the field is null) instead of hardcoding.
- **Best-candidate selection.** `pickMarker` ranks candidates by `(submission_count desc,
  confidence desc)` after duration filtering, instead of first-usable.
- **Per-segment confidence in the payload.** Replace the single `Confidence *float64` on
  `markers.MarkerUpdatePayload` with per-segment confidence (e.g. `IntroConfidence`,
  `CreditsConfidence`, …) so a merged multi-source result writes the correct
  `{kind}_markers_confidence` per kind. Update `BuildUpdatePayload` and the
  `scanner.FileRepository.UpsertMarkers` column mapping accordingly.

### Layer B — Multi-source dispatch ("query all, best wins")

- **`Submitter` capability (optional interface).**
  ```go
  // internal/markers/types.go
  type Submitter interface {
      Provider
      SubmitMarker(ctx context.Context, req SubmissionRequest) (SubmissionResult, error)
      // FetchUserStats validates the configured key and returns contribution stats.
      FetchUserStats(ctx context.Context) (UserStats, error)
  }
  ```
  Only `introdb.Provider` implements it; the contribution service type-asserts
  `provider.(markers.Submitter)`. This is also what makes contribution per-provider: the
  service only ever submits to registered providers that satisfy `Submitter` *and* are enabled
  in `marker_provider_config` (C6).

- **Merge dispatch.** Add `Registry.FetchMerged(ctx, req) (Result, bool, error)` that fans out
  to all *fetch-enabled* providers concurrently (bounded), then merges **per segment kind**
  keeping the candidate with the highest `(submission_count, confidence)`. `FetchFirstHit`
  stays as the trivial single-provider path / fallback. With only TheIntroDB enabled,
  `FetchMerged` behaves identically to today — zero behavior change until a second source
  exists.

- **Cross-source-class reconciliation is unchanged.** `FetchMerged` reconciles *within* the
  online class; `CanWriteMarker` still arbitrates online vs scanner vs manual at write time.
  No double logic.

- **Provider enable/order config.** Driven by the `marker_provider_config` table (C6):
  `fetch_enabled` selects which providers are queried, `fetch_priority` is the merge
  tiebreaker. The metadata `library_provider_chains` DB pattern (`internal/metadata/chain.go`)
  is the upgrade path *if* per-library control is ever needed — noted, not built.

- **Future plugin sources (deferred seam).** A `marker_provider.v1` plugin capability would let
  third-party sources plug in. The clean seam is a `PluginMarkerProvider` adapter implementing
  `markers.Provider` (mirroring `internal/metadata/plugin_provider.go`), so the registry never
  changes. Cross-repo (silo-plugin-sdk) — explicitly out of scope here.

### Layer C — Contribution to TheIntroDB

#### C1. Submission client

Add to `internal/markers/introdb/client.go`:
- `SubmitSegment(ctx, SubmissionRequest) (SubmissionResult, error)` → `POST /v3/submit`,
  Bearer key **required** (error early if `introdb.api_key` is empty), usage-limit aware.
- `FetchUserStats(ctx) (UserStats, error)` → `GET /v3/user/stats`.
`introdb.Provider` implements `markers.Submitter` by delegating to these.

#### C2. What is eligible to contribute

A marker is contributable iff:
- the target provider is a registered `Submitter` with `contribute_enabled = true` (C6), and
- its segment source ∈ `{scanner, manual}` (**never** `online` — that came *from* an online
  provider; contributing it back is circular), and
- the file resolves to a usable external ID (tmdb/tvdb/imdb) via the existing resolver, and
- for **auto** contribution: provider `contribute_auto_local = true`, source = `scanner`,
  kind = `intro`, confidence ≥ that provider's `contribute_min_confidence`. (Local detection is
  intro-only/episodes-only.)
- for **manual** contribution: any kind, episodes or movies, source = `manual`.

#### C3. Manual marker API (write path; UI deferred)

Markers live on `media_files`, and an item can have several versions, so the manual API is
**file-scoped** for precision, with an item-level convenience resolver. All under the existing
`RequireAdmin` group (`internal/api/middleware/auth.go`). New `internal/api/handlers/admin_markers.go`:

- `GET /admin/files/{fileId}/markers` — current per-segment values **plus full provenance**
  (`source`, `provider`, `confidence`, `algorithm`, `detected_at`) for each of
  intro/recap/credits/preview. The read side an editor (or a script) needs.
- `PUT /admin/files/{fileId}/markers` — upsert the **manual** layer. Body carries, per segment,
  either `{start, end}` (seconds) or `null` to clear that segment's manual value. Writes
  `source = manual` (priority 4, beats every other source) through the existing priority-gated
  `scanner.FileRepository.UpsertMarkers` path, then notifies live sessions via
  `playback.MarkerUpdateNotifier.MarkersUpdated` (mirrors `admin_intro.go`).
- `DELETE /admin/files/{fileId}/markers/{segment}` — explicit single-segment clear.
- `GET|PUT /admin/items/{id}/markers` — item-level convenience that resolves to the primary
  file (and reports the per-version set), for callers that don't track file IDs.

**Clear semantics.** Clearing a manual segment nulls that segment's value and provenance;
`markers_source` drops to the next-highest segment still present. A subsequent local
re-detection or online lazy-fetch can repopulate it. (We can't "un-overwrite" a value a manual
edit replaced, so clearing means "remove the manual value," not "restore the previous source.")

**Validation** mirrors the contribution rules so a manual edit can always be contributed:
`end > start`; intro/recap may omit start (= 0); credits/preview may omit end (= file
duration); values within [0, file duration].

A web editor (player-chrome control with a "use current time" helper) attaches to this API
later; its UX is intentionally **out of scope** for this round.

#### C4. Contribution tracking (idempotency + audit)

The audit log that makes submission idempotent and lets us report status. Per-provider by
construction (the `provider` column), so the same marker can be tracked/submitted to several
providers independently.

```sql
-- migrations/<next>_marker_contributions.up.sql  (next free number at implementation time)
CREATE TABLE public.marker_contributions (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    media_file_id      integer NOT NULL REFERENCES media_files(id) ON DELETE CASCADE,
    provider           text    NOT NULL,                 -- 'introdb'
    segment_kind       text    NOT NULL,                 -- intro|recap|credits|preview
    source             text    NOT NULL,                 -- what we contributed: 'scanner'|'manual'
    submitted_start_ms bigint,
    submitted_end_ms   bigint,
    video_duration_ms  bigint,
    content_hash       text    NOT NULL,                 -- hash(segment_kind,start_ms,end_ms,duration_ms)
    submission_id      uuid,                             -- returned by /submit
    status             text    NOT NULL,                 -- pending|accepted|rejected|error
    http_status        integer,
    error              text,
    submitted_at       timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (media_file_id, provider, segment_kind, content_hash)
);
CREATE INDEX marker_contributions_file_idx ON public.marker_contributions(media_file_id);
```

`content_hash` over the *value* gives idempotency: the same times are never resubmitted to the
same provider, but a manual correction (different times → different hash) submits the new
value — which is exactly how TheIntroDB's weighted averaging expects corrections to arrive.

#### C5. Contribution service + triggers

A single `internal/markers/contribute.go` `ContributionService` funnels both triggers:
`resolve enabled submitter providers (C6) → per provider: eligibility (C2) → idempotency check
(hash vs marker_contributions) → Submitter.SubmitMarker → persist result`. Triggers:
- **Manual / on-demand:** `POST /admin/files/{fileId}/contribute` (C7) submits a file's eligible
  markers to enabled providers (optionally scoped to one provider/segments).
- **Auto local detections:** a daily `ContributeMarkersTask` (`internal/taskmanager/tasks/`)
  that, for each provider with `contribute_enabled && contribute_auto_local`, scans for episode
  files with `intro_markers_source = 'scanner'` and `intro_markers_confidence ≥` that provider's
  `contribute_min_confidence` lacking a current-hash `marker_contributions` row, and submits
  them rate/usage-limited. Runs after the existing 03:30 detection task.

#### C6. Per-provider configuration

Contribution and fetch behavior is **per provider**, so a single global toggle won't do.
Credentials stay in `server_settings` (sensitive handling: `introdb.api_key`); behavioral
config moves to a new table keyed by provider id:

```sql
-- migrations/<next>_marker_provider_config.up.sql
CREATE TABLE public.marker_provider_config (
    provider                  text PRIMARY KEY,                -- 'introdb'
    fetch_enabled             boolean NOT NULL DEFAULT true,   -- queried during reads (Layer B)
    fetch_priority            integer NOT NULL DEFAULT 100,    -- merge tiebreaker
    contribute_enabled        boolean NOT NULL DEFAULT false,  -- master submit switch (off)
    contribute_auto_local     boolean NOT NULL DEFAULT false,  -- auto-submit local detections (off)
    contribute_min_confidence double precision NOT NULL DEFAULT 0.95,
    updated_at                timestamptz NOT NULL DEFAULT now()
);
INSERT INTO public.marker_provider_config (provider, fetch_enabled) VALUES ('introdb', true);
```

- A provider's submissions require `contribute_enabled = true`. With it on, **manual**
  corrections submit on demand; **auto** submission of local detections additionally requires
  `contribute_auto_local = true`. Both contribute flags default off — "easily enabled with a
  setting," exactly one row per provider.
- This same table carries the Layer B fetch enable/order, so "which providers do we read from"
  and "which do we submit to" live in one place. New providers insert a row (contribute off).
- Surfaced and edited through the provider API (C7), so the future admin surface lists
  providers generically rather than hard-coding `introdb`.

The `markers.mode` / `markers.lazy_playback` globals and the `introdb.api_key` credential are
unchanged.

#### C7. Admin provider + contribution API (fully fledged)

The complete backend surface, all under `RequireAdmin`, so a UI is purely additive later:

- `GET /admin/markers/providers` — list every registered provider: id, whether it is a
  `Submitter`, its `marker_provider_config` row, and (best-effort, cached) live `/user/stats`
  for submitter providers that have a key.
- `PUT /admin/markers/providers/{provider}` — update that provider's config row
  (`fetch_enabled`, `fetch_priority`, `contribute_enabled`, `contribute_auto_local`,
  `contribute_min_confidence`). Validated; unknown provider → 404.
- `POST /admin/markers/providers/{provider}/validate` — validate the configured key and return
  fresh `UserStats` (calls `Submitter.FetchUserStats`). Non-submitter → 400.
- `POST /admin/files/{fileId}/contribute` — submit this file's eligible markers (C2) to enabled
  submitter providers; optional body `{provider?, segments?[]}` to scope to one provider /
  specific kinds. Returns per (provider, segment) `SubmissionResult`. Honors idempotency (C4):
  unchanged values are skipped, not resubmitted.
- `GET /admin/files/{fileId}/contributions` — contribution history for the file from
  `marker_contributions` (per provider/segment: status, submitted_at, error).

Plus the manual marker endpoints from C3. Together these cover read, edit, submit, and audit
without any web code.

## Data model summary

- **New table `marker_contributions`** (C4) — idempotent submission audit, keyed per
  (file, provider, segment, value-hash).
- **New table `marker_provider_config`** (C6) — per-provider fetch + contribute behavior,
  seeded with `introdb` (fetch on, contribute off).
- **No new flat server settings.** `introdb.api_key`, `markers.mode`, `markers.lazy_playback`
  are unchanged; behavioral provider config lives in `marker_provider_config`, not string keys.
- **No new marker columns.** Per-segment confidence columns already exist; Layer A only changes
  how they're *populated* (per-segment instead of collapsed). `submission_count` stays
  transient (merge-ranking only) unless a later need appears.

## Client / cross-repo impact

- **No Android/Apple changes required.** This adds no new marker *kinds* and no new
  client-facing read contract: markers still flow to clients via the existing `WatchDetail`
  fields, the jellycompat `GET /MediaSegments` endpoint, and the `markers_updated` realtime
  event. Manual editing and contribution are server-side (admin API); their eventual UI is
  deferred web-admin work.
- **Plugin SDK:** untouched. `marker_provider.v1` is a deferred future capability (Layer B
  seam), not part of this work.
- A future *per-user* contribution model (each user links their own TheIntroDB account) *would*
  be client work (a "Connect TheIntroDB" screen) — explicitly deferred.

## Build sequence

Each phase is shippable on its own; later phases depend only on earlier ones. Every phase is
**backend-only** — no web UI.

1. **Phase 1 — Correctness foundation (Layer A).** TVDB lookups; capture
   confidence/submission_count; best-candidate selection; per-segment confidence in the
   payload. *Directly answers "are we doing this properly," low risk, no new surface.*
2. **Phase 2 — Multi-source dispatch + provider config (Layer B + C6).** `Submitter` interface;
   `FetchMerged` query-all/best-wins; `marker_provider_config` table + repo driving fetch
   enable/order. *No behavior change with only TheIntroDB enabled.*
3. **Phase 3 — Submission client + tracking + service (C1, C4, C5 core).** `SubmitSegment` /
   `FetchUserStats`; `marker_contributions` migration + repo; `ContributionService` (eligibility,
   idempotency, per-provider gating).
4. **Phase 4 — Manual marker API (C3) + provider/contribution API (C7).** File- and item-level
   manual read/write/clear; provider list/config/validate; per-file contribute + history. The
   full admin contract, no UI.
5. **Phase 5 — Auto-contribution (C5 task).** `ContributeMarkersTask`: per provider, when
   `contribute_enabled && contribute_auto_local`, submit episode intro markers above the
   provider's `contribute_min_confidence`, usage-limit aware, idempotent.

Deferred (separate UI-only follow-up): web admin surfaces over C3/C6/C7 — a marker editor in
the player chrome and a provider/contribution panel in admin settings.

## Risks & trade-offs

- **Data-quality / pollution.** Auto-submitting chromaprint detections (confidence 0.65–0.90)
  risks polluting a provider's data. Mitigation: `contribute_enabled` and `contribute_auto_local`
  default **off** per provider, and the default `contribute_min_confidence` of 0.95 admits only
  chapter / chapter+silence detections even once enabled. Manual, high-trust corrections are the
  intended first contribution. Revisit chromaprint auto-submission after watching acceptance
  rates via `/user/stats`.
- **Single-account weighting.** All contributions credit one server account; TheIntroDB's
  weighting may treat a single account's mass submissions differently than many users'. Note
  the per-user seam; acceptable for v1.
- **Circular contribution.** Never contribute `source = online`. Enforced in C2 eligibility and
  worth a unit test.
- **Usage limits.** Bulk auto-submission can hit the submission usage limit; the task must obey
  `X-UsageLimit-Reset` and resume next run. Idempotency means an interrupted run safely resumes.
- **"Query all" cost.** `FetchMerged` issues one lookup per fetch-enabled provider; with only
  TheIntroDB it's identical to today, and per-provider caching bounds the cost as sources are
  added.
- **Movies & non-intro segments** have no local detector, so their only contribution route is
  manual correction — acceptable and explicit.

## Open questions for review

1. **Contribute to all enabled providers, or require explicit per-call selection?** The design
   defaults `POST /admin/files/{id}/contribute` to "every enabled submitter," with an optional
   `provider` scope. Acceptable, or should submission always name its target provider?
2. **Manual-clear behavior.** Clearing a manual segment removes the manual value and lets a
   later detection/fetch repopulate it (we can't restore the exact pre-edit value). Is
   "remove, then re-detect" the right contract, or do you want a marker history/undo stack?
3. **Per-provider config home.** This design uses a `marker_provider_config` table (clean for N
   providers, future per-library override) rather than flat `server_settings` keys. Confirm the
   table is the direction you want.

## File-level blueprint

**Backend (Go) — Layer A**
- `internal/markers/introdb/client.go` — *modify*: `tvdb_id` query support; cache-key extension.
- `internal/markers/introdb/types.go` — *modify*: `confidence`/`submission_count` fields.
- `internal/markers/introdb/provider.go` — *modify*: read TVDB; real confidence; best-candidate pick.
- `internal/markers/types.go` — *modify*: per-segment confidence in `Marker`/Result plumbing.
- `internal/markers/write.go` — *modify*: per-segment confidence in `MarkerUpdatePayload` + `BuildUpdatePayload`.
- `internal/scanner/file_repo.go` — *modify*: map per-segment confidence in `UpsertMarkers`.

**Backend (Go) — Layer B + per-provider config**
- `internal/markers/types.go` — *modify*: `Submitter` interface; `SubmissionRequest`/`Result`, `UserStats`; `Registry.FetchMerged`.
- `internal/markers/provider_config.go` — *create*: `marker_provider_config` repo (load/update; drives fetch enable/order + contribute gating).
- `migrations/<next>_marker_provider_config.{up,down}.sql` — *create*.

**Backend (Go) — Layer C**
- `internal/markers/introdb/client.go` — *modify*: `SubmitSegment` (`POST /v3/submit`), `FetchUserStats` (`GET /v3/user/stats`), usage-limit handling.
- `internal/markers/introdb/provider.go` — *modify*: implement `markers.Submitter`; drop the "submissions not supported" note in `types.go`.
- `internal/markers/contribute.go` — *create*: `ContributionService` (eligibility, idempotency, per-provider gating, submit, persist).
- `internal/markers/contribution_repo.go` — *create*: `marker_contributions` CRUD + value-hash check.
- `migrations/<next>_marker_contributions.{up,down}.sql` — *create*.
- `internal/api/handlers/admin_markers.go` — *create*: manual marker read/write/clear (C3) + per-file contribute + history (C7 file endpoints).
- `internal/api/handlers/admin_marker_providers.go` — *create*: provider list/config/validate (C7 provider endpoints).
- `internal/api/router.go` — *modify*: register the new `RequireAdmin` routes.
- `internal/taskmanager/tasks/contribute_markers.go` — *create*: daily auto-contribution task.
- `cmd/silo/main.go` — *modify*: wire `ContributionService`, provider-config repo, the task, and the `Submitter`.

**Frontend (React/TS) — deferred (not this round)**
- Marker editor over `PUT /admin/files/{id}/markers`; provider/contribution panel over the C7
  endpoints; `IntroDBCredentialCard` "validate key & show stats". Listed for traceability only.

**Reference (read-only, not in this repo)**
- TheIntroDB v3 contract verified against `github.com/TheIntroDB/theintrodb-npm`
  (`src/funcs.ts`, `src/types.ts`, `docs/`) and `github.com/TheIntroDB/jellyfin-plugin`
  (`TheIntroDB/Api/*.cs`).
