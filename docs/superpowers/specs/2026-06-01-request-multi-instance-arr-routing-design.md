# Multi-Instance Sonarr/Radarr Routing for Requests (Seerr-style)

**Status:** Design approved, ready for implementation plan
**Date:** 2026-06-01
**Area:** `internal/requests`, `migrations/`, `web/src` (admin settings + request queue)

## Summary

Silo's request system today fulfills every approved request through a single
Radarr (movies) or single Sonarr (series) instance, at whatever quality that
one instance's profile is configured for. There is no 4K concept, no anime
handling, and exactly one instance per kind (`request_integrations.kind` is the
primary key).

This work replicates Seerr's Sonarr/Radarr management model *inside* Silo's own
request system (no external Seerr dependency):

- **Many instances per kind** — N Radarr and N Sonarr servers, each with its own
  connection, root folder, quality profile, and tags.
- **Default HD + Default 4K routing** — one HD-default and one 4K-default
  instance per kind; requests route to the matching default(s).
- **Per-server anime overrides** (Overseerr-style) — each instance carries an
  optional anime profile / root folder / tags applied when a title is detected
  as anime.
- **Entitlement-driven dual-quality fan-out** — a single user request can be
  fulfilled in **both** 1080p and 2160p when the requester's `MaxPlaybackQuality`
  allows 4K, or when an admin global toggle forces it for everyone.

The end-user request action is unchanged: quality is derived from the user's
existing entitlement, not a per-request choice.

## Goals

- Support multiple Radarr/Sonarr instances per kind.
- Route by quality (HD vs 4K) to per-kind default instances.
- Fan a single request out to both 1080p and 2160p targets based on the
  requester's `MaxPlaybackQuality` entitlement, with an admin override that
  forces dual-quality regardless of role.
- Apply per-instance anime overrides using Seerr-exact anime detection.
- Keep the request lifecycle (quotas, approval, reconcile) coherent under
  one-to-many fulfillment.

## Non-Goals

- No integration *with* a Seerr instance (an earlier idea, dropped). Silo
  remains the system of record.
- No per-request manual server/profile picker (Overseerr's advanced UI). Routing
  is automatic from defaults + entitlement.
- No separate 1080p/4K *availability* tracking. The pre-request presence check
  stays binary ("is this title in the Silo library at all").
- No admin-configurable anime keyword list (the heuristic is isolated so one can
  be added later).
- No dedicated-anime-instance flag — anime is handled by per-instance overrides
  (decided during design).

## Background: current flow

When a user requests a movie today (`internal/requests/service.go`):

1. **Browse/discover** via TMDB; each result carries a `RequestState`.
2. `CreateRequest` validates: requests enabled, not already available
   (presence), not already actively requested (dedup by `tmdb_id`), computes the
   requester's `EffectivePolicy` (quota, blocked, auto-approve), enforces quota.
3. Auto-approve users with a configured integration → row created `approved`,
   else `pending` for the admin queue.
4. `submitApprovedRequest` → `integrationForMediaType(mediaType)` finds the
   single instance by kind → `radarr.Client.SubmitMovie` POSTs to Radarr using
   that instance's root folder / quality profile / tags → request goes `queued`.
5. `ReconcileRequests` polls the instance: `queued → downloading → completed`
   (or `failed`).

The single insertion point that changes is step 4's instance selection.

## Design

### 1. Data model

**`request_integrations` — reworked from one-row-per-kind to many instances:**

| column | purpose |
|---|---|
| `id` (text PK, via `idgen`) | replaces `kind` as PK |
| `kind` (text, `radarr`\|`sonarr`) | now a plain column, keeps its CHECK |
| `name` (text) | admin-facing label |
| `enabled` (bool) | |
| `base_url`, `api_key_ref` | connection; API key stays a Fernet secret ref, never plaintext |
| `root_folder`, `quality_profile_id`, `tags[]` | standard (non-anime) defaults |
| `is_4k` (bool) | marks this as a 4K server |
| `is_default` (bool) | the HD default for its kind |
| `is_default_4k` (bool) | the 4K default for its kind |
| `anime_enabled` (bool) | apply anime overrides on this instance |
| `anime_quality_profile_id`, `anime_root_folder`, `anime_tags[]` | anime overrides |
| `options` (jsonb) | kind-specific extras (Radarr min-availability, Sonarr season-folders/series-type) |
| `last_check_*`, `created_at`, `updated_at` | unchanged |

**Invariants** (partial unique indexes + service validation):

- At most one `is_default` per kind; at most one `is_default_4k` per kind.
- `is_default ⇒ NOT is_4k` and `is_default_4k ⇒ is_4k`. The HD default is an HD
  server; the 4K default is a 4K server. HD and 4K defaults are therefore always
  distinct instances.

**New `media_request_targets`:**

| column | purpose |
|---|---|
| `id` (bigint identity PK) | |
| `request_id` (text → `media_requests` ON DELETE CASCADE) | |
| `integration_id` (text → `request_integrations` ON DELETE SET NULL) | |
| `integration_kind` (text snapshot) | survives instance deletion for history |
| `quality` (text, `1080p`\|`2160p`) | reuses `internal/access` presets |
| `is_anime` (bool) | which profile set was applied |
| `external_id`, `external_status`, `status`, `last_error` | per-target Radarr/Sonarr lifecycle |
| `created_at`, `updated_at` | |
| UNIQUE `(request_id, quality)` | one target per quality per request |

**`media_requests` changes:** the per-fulfillment columns
(`integration_kind`, `external_id`, `external_status`) move out to
`media_request_targets`. Add `is_anime` (bool). The request's `status` becomes an
**aggregate** over its targets (see §3). `idx_media_requests_active_tmdb` stays —
dedup is still one active request per title regardless of quality, and a
dual-quality request counts as **one** row against the user's quota.

### 2. Routing engine

A pure function `routeTargets(req, entitlement, settings, instances) -> []plannedTarget`
replaces `integrationForMediaType` as the selection point in
`submitApprovedRequest`.

**Inputs:** the request (incl. detected `is_anime`), the requester's
`MaxPlaybackQuality`, the global `force_dual_quality` setting, the configured
instances for the kind.

**Algorithm:**

1. **Kind** = `movie → radarr`, `series → sonarr`.
2. **Desired qualities:**
   - `1080p` is always desired (baseline for everyone).
   - Add `2160p` if `access.QualityAllowed("2160p", user.MaxPlaybackQuality)`
     **OR** `force_dual_quality` is on (the toggle overrides role).
3. **Pick the default instance per desired quality:** `1080p → is_default`,
   `2160p → is_default_4k` (for that kind).
4. **Profile selection per chosen instance:** if `req.is_anime` **and** the
   instance has `anime_enabled` → use its anime profile / root folder / tags
   (and, for Sonarr, `seriesType: anime`); otherwise the standard set.
5. Emit one `plannedTarget{instance, quality, isAnime, profile, rootFolder, tags}`
   per resolved quality.

**Edge cases:**

- A desired quality with **no default instance assigned** is silently skipped
  (cannot route 4K without a 4K default — matches "when both are assigned").
- **Zero** resolved targets (e.g. no HD default configured): the request stays
  `approved` but unfulfilled, with `last_error` = "no Radarr/Sonarr instance
  configured for this quality" so it surfaces in the admin queue.
- No dedup needed: the `is_default`/`is_default_4k` invariants guarantee HD and
  4K defaults are distinct instances.

### 3. Fulfillment & reconcile lifecycle

The adapter interfaces (`SubmitMovie`/`SubmitSeries`,
`CheckMovieStatus`/`CheckSeriesStatus`) are **unchanged** — they already take
`(req, integration)`. We call them **once per target**, passing a *resolved*
`Integration` whose `RootFolder`/`QualityProfileID`/`Tags` are filled from the
standard or anime block. Radarr/Sonarr's notion of "4K" is just that instance's
quality profile + root folder, so adapters stay quality-agnostic. (The Sonarr
adapter additionally sets `seriesType: anime` for anime targets.)

**On approval (`submitApprovedRequest`):**

1. `routeTargets(...)` → planned targets.
2. For each: insert a `media_request_targets` row, then call the matching
   adapter with the resolved instance + decrypted key.
3. On success → target `queued` with its `external_id`/`external_status`; on
   error → target `failed` + `last_error`.
4. **Recompute the request aggregate** in the same transaction as the target
   writes.

**Reconcile loop (`ReconcileRequests`):** for each candidate request, iterate its
**non-terminal targets**, load each target's instance by `integration_id`, call
the status adapter, update the target, then recompute the aggregate. Targets
reconcile independently.

**Aggregate recompute (single source of truth):**

- all targets `completed` → request `completed` (+ `completed_at`)
- any target `downloading` → `downloading`; else any `queued` → `queued`
- all targets `failed` → `outcome = failed`; **some** failed while others active
  → request stays active, `last_error` set from the failed target(s)

**Retry** is target-aware: an admin retry re-submits only the `failed` targets
(re-routing if instance config changed), leaving healthy targets alone.

### 4. Anime detection (Seerr-exact)

Detection matches upstream Seerr exactly — a single TMDB keyword id, no genre or
language fallback:

- `server/api/themoviedb/constants.ts`: `ANIME_KEYWORD_ID = 210024`
- `server/entity/MediaRequest.ts`: anime ⇔ `keywords.results` contains that id.

Implementation:

- A named constant `animeKeywordID = 210024`.
- `detectAnime(detail) bool` → true iff the TMDB keyword **ids** contain
  `210024`. Matching by id (not the name string) avoids false positives from
  neighboring keywords like "based on anime".
- Detected once at `CreateRequest` time and stored on `media_requests.is_anime`,
  so routing, reconcile, and retries stay consistent.
- Applies to both movies and series; ignored if no instance for that kind has
  `anime_enabled`.

**Supporting TMDB-client change:** extend `tmdb.MediaDetail` to carry keyword
**ids** (add `append_to_response=keywords` to the detail fetch). `GetMediaDetail`
is already called in the request path, so no extra round-trip beyond the append.
(`OriginalLanguage` is not needed under the parity-only decision but may be
mapped opportunistically since it is already in the response.)

The heuristic is isolated in one function so an admin-configurable keyword list
can be added later without touching routing.

### 5. Admin UI

`web/src/pages/admin-settings/IntegrationsSettings.tsx` and the setup wizard
`web/src/pages/setup-wizard/steps/IntegrationsStep.tsx` move from a single
Radarr/Sonarr form to an **instance-list manager** per kind:

**Per kind — list of instance cards** (add / edit / delete):

- Connection: name, base URL, API key (write-only; shows "configured" once set,
  never echoes the secret), **Test connection** → `LoadIntegrationOptions`
  populates root-folder and quality-profile **dropdowns**.
- Standard block: root folder, quality profile, tags.
- Quality role: `is_4k` switch, **Default (HD)** toggle, **Default 4K** toggle.
  The form enforces invariants client-side (no Default-HD on a 4K server;
  selecting a new default clears the prior one visually); server is source of
  truth.
- Anime block (collapsible, `anime_enabled`): anime quality profile, anime root
  folder, anime tags — dropdowns from the same test-connection options.

**Request settings panel** gains the **"Always fulfill in both 1080p and 4K"**
toggle (`force_dual_quality`) with helper text: applies to all requests when both
a Default HD and Default 4K instance exist, regardless of user role.

**`web/src/pages/AdminRequests.tsx`** (queue): each request row expands to show
its **targets** — quality badge (1080p/2160p), instance name, per-target status,
and per-target **retry** on failed ones. The aggregate status stays the headline.

**Setup wizard** stays minimal: add one Radarr + one Sonarr, auto-marked Default
HD. Advanced multi-instance/4K/anime config lives in the full settings page.

### 6. API contract & multi-repo coordination

**End-user request flow is unchanged** — quality is derived from
`MaxPlaybackQuality`, so the request action and payload are identical (no 4K
toggle, no new permission).

**`Request` response model** gains `is_anime` and a `targets[]` array
(`quality`, `instance_name`, `status`, `external_status`, `last_error`); the
top-level `integration_kind`/`external_id`/`external_status` are removed in favor
of the aggregate `status` + `targets`.

- Per `CLAUDE.md`'s multi-repo rule: end-user `silo-android` / `silo-apple`
  clients show a single aggregate status, so they likely need **no change** —
  but this is flagged as explicit Apple/Android follow-up to verify the request
  model tolerates the removed fields and ignores/parses the new `targets`.

**Admin endpoints** change from "upsert by kind" to instance CRUD:

- `GET/POST/PUT/DELETE` request integrations **by `id`**; `LoadIntegrationOptions`
  (test-connection) stays, keyed by instance.
- Default-toggle handled server-side transactionally (a new HD/4K default clears
  the prior one for that kind).
- `request_settings` gains `force_dual_quality`.
- Admin-web-only; no mobile client surface.

### 7. Migration & backfill

Next migration number: **169** (`169_request_multi_instance.{up,down}.sql`).

**Up:**

1. `ALTER request_integrations`: add `id`, `name`, `is_4k`, `is_default`,
   `is_default_4k`, `anime_enabled`, `anime_quality_profile_id`,
   `anime_root_folder`, `anime_tags[]`. Backfill existing rows: `id = gen`,
   `name = initcap(kind)`, `is_default = enabled` (the lone existing instance
   becomes that kind's HD default), everything else default/false. Swap the PK
   from `kind` to `id`; keep `kind` as a plain column with its CHECK.
2. Partial unique indexes: one `is_default` per kind, one `is_default_4k` per
   kind.
3. `CREATE media_request_targets` (per §1).
4. Backfill targets: for any `media_requests` row with an `external_id`, insert a
   target `(quality='1080p', is_anime=false, integration_id = that kind's
   instance, integration_kind, external_id, external_status, status = request's
   current status)`. Rows never submitted get no target yet.
5. `ALTER media_requests`: add `is_anime bool DEFAULT false`; drop
   `integration_kind`, `external_id`, `external_status`.
6. `idx_media_requests_active_tmdb` stays unchanged.

**Down:** re-add the three columns to `media_requests` and copy back the **1080p**
target's external fields; drop `media_request_targets`; drop `is_anime`; collapse
`request_integrations` to `kind`-PK by keeping each kind's `is_default` instance
and discarding extras; drop the new columns/indexes.

**Caveat (explicit):** the down migration is **lossy** — rollback discards any
additional instances (beyond one default per kind) and any 4K/anime target
history. This is the unavoidable cost of collapsing a one-to-many back to
one-to-one and is acceptable for a down migration.

**Go-side data access** (implementation, not SQL): `Integration` struct +
`scanIntegration` gain the new fields; the `Store` interface gains target CRUD +
`ListInstances(kind)`; `scanRequest` drops the external fields; `UpsertIntegration`
becomes id-based CRUD with default-toggle handling in-transaction.

### 8. Testing

- **`routeTargets` — table-driven (core of the feature):** matrix of
  {1080p vs 2160p entitlement} × {`force_dual_quality` on/off} × {which defaults
  exist} × {anime vs not} → expected target set.
- **`detectAnime`:** keyword `210024` present/absent; no name-substring false
  positives.
- **Aggregate recompute:** all-complete, partial-failure (request stays active +
  `last_error`), all-failed (`outcome=failed`), mixed downloading/queued.
- **Repository:** target CRUD; single-default-per-kind invariant rejects a second
  default; migration backfill yields exactly one 1080p target per previously
  submitted request.
- **Adapter:** Sonarr `SubmitSeries` sets `seriesType=anime` only for anime
  targets on anime-enabled instances.
- **Service:** extend `service_test.go` fakes — a 4K-entitled user's single
  request fans out to two adapter submissions with the right resolved instances;
  target-scoped retry re-submits only the failed target.
- **Frontend:** instance-form default-invariant enforcement; queue renders
  per-target rows + retry.

## Risks & open considerations

- **Reconcile cost** scales with targets, not requests (≤2× today). Bounded and
  acceptable; the reconcile loop already batches.
- **Anime keyword imperfection** is inherited from Seerr by design (parity
  choice). Mitigated by isolating `detectAnime` for future tuning.
- **Client model drift**: removing top-level external fields from `Request` must
  be verified against `silo-android` / `silo-apple` deserialization before
  release (flagged in §6).

## References

- Seerr upstream: https://github.com/seerr-team/seerr
  (`server/api/themoviedb/constants.ts`, `server/entity/MediaRequest.ts`)
- Overseerr anime discussions: #2876 (Sonarr for anime only), #3777 (configurable
  series type)
- Existing code: `internal/requests/{service,repository,types,store}.go`,
  `internal/requests/{radarr,sonarr,arrclient}`, `internal/access/quality.go`,
  `migrations/139_media_requests.up.sql`
