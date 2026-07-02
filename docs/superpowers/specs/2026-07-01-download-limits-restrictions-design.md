# Download limits & restrictions (downloads v2) — design

**Goal:** Extend downloads v2 with the restriction knobs admins actually need: (1) a **quality
ceiling** — server-wide and per-user — so a user can be allowed to download, even transcode, but
only up to a capped bitrate; (2) a **batch size cap** protecting the server from a single series
request fanning out into hundreds of rows and transcode jobs; (3) **per-user overrides** for the
existing global quantity and bandwidth limits, following the established `users.max_streams` /
`users.max_transcodes` pattern, so households can tier users without changing the global policy.

**Status:** Draft — design input for a v1 capability proposal (Silo v1 scope gate,
`docs/architecture/v1-scope.md`). All API changes are additive within `/api/v1`.

Decisions locked with the requester:
- **No device/user storage caps.** Device disk is the client's job (per-subscription
  `max_storage_bytes` already covers the monitoring case); server disk is already covered by
  `download.artifact_max_bytes`; serving volume is already covered by bandwidth + per-period quotas.
- **No expiry / lease / revocation of already-downloaded files.** Unchanged from the downloads v2
  design (`docs/superpowers/specs/2026-06-18-offline-sync-mobile-design.md`).
- **Transcode restriction already exists** (`download.transcode_enabled` +
  `users.download_transcode_allowed`) and is kept as-is; the quality ceiling composes with it
  ("transcode allowed, but only up to 5mbps").
- **Admin observability (downloads dashboard, admin list/revoke endpoints) is a separate effort**,
  not part of this spec.

> Commands and paths in this document are repository-relative; assume the repository root is the cwd.

## Current state (what this builds on)

Enforcement today lives in `internal/downloads`:

- **Policy** — `policy.go`: `DownloadQualityResolver.Resolve` validates a requested quality from the
  ladder `original > 20mbps > 10mbps > 5mbps > 2mbps > 1mbps` and gates bitrate presets on
  `cfg.TranscodeEnabled && user.DownloadTranscodeAllowed`. `PresetsFor` advertises the fulfillable
  ladder through `GET /api/v1/downloads/capability` (`quality_presets`), so clients render only what
  the server will honor.
- **Quantity** — `limiter.go`: `QuantityLimiter.Check(ctx, userID, batchSize)` enforces the global
  `download.max_concurrent_per_user` and `download.max_per_period` / `download.period_duration`
  settings (0 = unlimited) against `CountActiveByUser` / `CountByUserSince`.
- **Bandwidth** — `bandwidth.go`: `BandwidthManager` token-buckets file serving with one server-wide
  rate and one per-user rate (`download.server_bandwidth_mbps`, `download.user_bandwidth_mbps`),
  caching one `rate.Limiter` per user id.
- **Per-user flags** — `users.download_allowed`, `users.download_transcode_allowed`
  (`internal/auth/repository.go`), exposed on the admin users API (`internal/api/handlers/admin.go`)
  and web UI (`web/src/pages/AdminUserDetail.tsx`).
- **Bulk** — `service.go` `createSeriesScoped` fans a series/season request into managed entries with
  no upper bound; `resolveBulkQuality` intentionally keeps batches **original-only**
  (`ErrBulkQualityUnavailable`). Subscription sync auto-registers in-scope episodes without
  consuming the quantity limiter (by design).
- **Serve-time re-check** — `Service.ServeFile` re-resolves policy on every file request, so
  disabling downloads or revoking a user flag takes effect immediately for anything not yet pulled.

Settings are freeform keys with defaults and validation in `internal/config/db_loader.go`, hot-
reloaded by `Service.loadConfig` (which also reloads the limiter and bandwidth manager on change),
and edited in `web/src/pages/admin-settings/DownloadSettings.tsx`.

## 1. Quality ceiling

### Semantics

- New server setting **`download.max_quality`** (string; `""` = no cap; otherwise one of the quality
  ladder values). New nullable per-user column **`users.download_max_quality`** (`NULL` = inherit the
  server setting; otherwise one of the ladder values).
- The **effective ceiling** for a user is the *lower* of the server setting and the user column on
  the ordered ladder (`original` is the top). A ceiling below `original` deliberately forbids
  `original` — that is the point: "this user gets at most a 5mbps transcode, never the full file."
- Enforcement is **presets-first**: `PresetsFor` truncates the advertised ladder at the effective
  ceiling, so compliant clients never offer a forbidden quality and **no client changes are
  required** (clients already read `quality_presets` from the capability endpoint).
- `Resolve` independently rejects any request above the ceiling with a new sentinel
  `ErrQualityNotPermitted` (handler maps it alongside the existing limit errors as HTTP 403,
  distinct from `ErrInvalidQuality`), so a non-compliant client cannot bypass the cap.
- The **original-compatibility fallback** inside `Resolve` (original → remux/transcode when the
  device can't play the source) must clamp its chosen `EffectiveQuality`/bitrate to the ceiling.
  In practice a sub-original ceiling makes the fallback moot — `original` is already rejected — but
  the clamp keeps the invariant local to `Resolve` rather than depending on call-site ordering.
- **Serve-time**: the existing `ServeFile` policy re-check additionally denies rows whose
  `effective_quality` exceeds the user's *current* ceiling. Consistent with today's revocation
  semantics: lowering a cap stops future serving of higher-quality registered entries; files already
  on-device are untouched (no expiry).

### Interactions (documented behavior, not bugs)

- **Ceiling below `original` + `download_transcode_allowed = false`** → the fulfillable preset list
  is empty and the capability endpoint reports no presets; the user effectively cannot download.
  This is legal admin configuration. The admin UI shows a hint on the per-user field when this
  combination is selected ("a sub-original cap requires the transcode permission").
- **Series/season/monitoring downloads are original-only today** (`resolveBulkQuality`). A user
  capped below `original` therefore cannot create series/season batches or receive subscription
  auto-registrations; those paths return `ErrQualityNotPermitted` (create) or skip registration with
  a `SkippedDownload` reason `quality_not_permitted` (subscription sync). This is accepted for now
  and called out here so it is not rediscovered as a bug. Lifting it means bulk transcode — a
  deliberate follow-up, not smuggled into this change.

### Implementation

- `Resolve`/`PresetsFor` gain the ceiling input. Rather than growing the parameter lists further,
  introduce a small `PolicyInput`/`userPolicy` struct (user + cfg + artifactsAvailable) if the
  signatures get unwieldy — implementer's choice; behavior is what is specified above.
- Ladder comparison helper in `internal/downloads/model.go` (e.g. `QualityAtMost(q, ceiling) bool`
  using the `QualityPresets` order) — no string parsing of bitrates.
- Validation: `db_loader.go` rejects a `download.max_quality` value not on the ladder; the users
  API rejects invalid `download_max_quality` the same way (shared validation via
  `downloads.ValidQuality`, with `""`/`NULL` allowed).

## 2. Batch size cap

- New server setting **`download.max_batch_size`** (int; default **100**; 0 = unlimited): the
  maximum number of managed entries one explicit series/season create may register, and the maximum
  auto-registered per subscription-sync pass.
- **Explicit series/season create** (`createSeriesScoped`): if the in-scope episode count (after
  access filtering and skip computation) exceeds the cap, **reject** the whole request with a new
  sentinel `ErrBatchTooLarge` (HTTP 400, error body includes the cap so clients can message "try a
  season at a time or subscribe"). Rejection is chosen over truncation because a silently partial
  one-shot batch never self-heals — there is no later pass that picks up the remainder.
- **Subscription sync** (`subscription_service.go`): **truncate** each pass at the cap (stable
  episode order). Truncation is correct here because sync recomputes in-scope-but-unregistered
  episodes every pass, so the remainder registers on subsequent syncs — natural pagination, and one
  monster series cannot monopolize a sync pass or the artifact queue.
- The capability response additively gains **`max_batch_size`** (int, 0 = unlimited) so clients can
  preflight ("this series has 340 episodes; the server accepts 100 per request") instead of
  discovering the limit by error.

## 3. Per-user quantity & bandwidth overrides

- Three new nullable columns on `users`, mirroring the `max_streams` pattern
  (`internal/models/user.go`, `internal/auth/repository.go`):
  - `download_max_concurrent int` — overrides `download.max_concurrent_per_user`
  - `download_max_per_period int` — overrides `download.max_per_period` (window stays the global
    `download.period_duration`; a per-user window is deliberately out of scope)
  - `download_bandwidth_mbps int` — overrides `download.user_bandwidth_mbps`
- Semantics: `NULL` = inherit the global setting; **0 = explicitly unlimited** (matches the global
  settings' zero-means-unlimited convention, and is the reason `NULL` and `0` must stay distinct).
- **QuantityLimiter**: `Check` gains the caller-resolved user overrides — e.g.
  `Check(ctx, userID, batchSize, overrides Overrides)` where `Overrides` carries the two optional
  ints. Every existing call site already has the `*models.User` loaded (the create paths load it
  for policy), so resolution stays in `service.go` and the limiter remains storage-free.
- **BandwidthManager**: the per-user limiter cache currently assumes one global rate. Store the
  rate alongside each cached limiter and recreate the entry when the effective rate for that user
  changes (`ThrottledReader` gains the resolved per-user bps from the caller; `ServeFile` already
  loads the user for its policy re-check). `Reload` keeps its existing clear-all behavior.
- **Admin API**: the three fields (plus `download_max_quality` from §1) are added to the admin user
  create/update/response DTOs in `internal/api/handlers/admin.go` and to
  `UpdateUserInput`/`CreateUserInput` in `internal/models/user.go` +
  `internal/auth/repository.go` — the exact `max_streams` additive pattern.

## Data model (Goose migration)

One timestamped migration (`make migrate-create NAME=user_download_limits`):

```sql
ALTER TABLE users
    ADD COLUMN download_max_quality    text,
    ADD COLUMN download_max_concurrent integer,
    ADD COLUMN download_max_per_period integer,
    ADD COLUMN download_bandwidth_mbps integer;
```

All nullable, no defaults, no backfill — `NULL` means "inherit global" for every column. The two new
settings keys (`download.max_quality`, `download.max_batch_size`) are settings rows, not schema.

## API surface (all additive)

- `GET /api/v1/downloads/capability` — `quality_presets` now reflects the effective ceiling
  (existing field, narrowed values); new field `max_batch_size`.
- `POST /api/v1/downloads` — new 403 error case (`quality_not_permitted`) and new 400 error case
  (`batch_too_large`, body includes the configured cap).
- `POST /api/v1/downloads/subscriptions/sync` — skipped registrations may carry the new
  `SkippedDownload` reason `quality_not_permitted`.
- Admin users endpoints — four new optional request fields / response fields (§1, §3).
- Settings endpoints — two new keys; both hot-reloadable (no restart-required entry in
  `internal/config/restart_keys.go`).

## Web admin UI

- `web/src/pages/admin-settings/DownloadSettings.tsx`: "Max Download Quality" (select over the
  ladder + "No limit") under Quantity Limits or a new Restrictions group; "Max Batch Size" numeric
  field with the 0-=-unlimited hint.
- `web/src/pages/AdminUserDetail.tsx` (+ the user edit dialog): rows for the four per-user fields,
  each showing the inherited global value when unset (as the streams/transcodes fields do), plus the
  §1 hint when a sub-original cap is combined with transcode-not-allowed.

## Enforcement summary

| Restriction | Hard/soft | Enforced at |
|---|---|---|
| Quality ceiling | Hard | `Resolve` (create), `PresetsFor` (capability), `ServeFile` re-check |
| Batch size cap | Hard | `createSeriesScoped` (reject), subscription sync (truncate per pass) |
| Concurrent / per-period overrides | Hard | `QuantityLimiter.Check` on every create path |
| Bandwidth override | Hard | `ThrottledReader` on every byte served |

Nothing in this spec is client-enforced; the client-pull architecture is unchanged.

## Client follow-up (silo-android, silo-apple)

- **Quality ceiling / presets:** zero work if the client renders `quality_presets` from the
  capability endpoint (the downloads v2 contract). Audit for hardcoded ladders.
- **Batch errors:** handle `batch_too_large` with a useful prompt (download per season / subscribe
  instead of a raw error toast). Handle empty `quality_presets` as "downloads unavailable for this
  account", which the capability contract already implies.
- No manifest or sync-protocol changes.

## Testing

- `policy_test.go`: ceiling truncation of `PresetsFor` (server-only, user-only, both, ties at
  `original`); `Resolve` rejection above the ceiling; fallback clamp; the
  cap-without-transcode-permission → empty presets case.
- `limiter_test.go` (new cases): override precedence NULL/0/positive vs. global for both limits.
- `service_*_test.go`: batch reject at cap boundary (cap, cap+1); subscription sync truncation and
  next-pass continuation; serve-time denial after a cap is lowered.
- Handler tests for the new error mappings and admin-user field round-trips — run in a
  libvips-capable container (bare-host `go test ./...` skips `internal/api/handlers`).

## Out of scope / follow-ups

- Admin downloads dashboard + admin list/revoke endpoints (observability; separate spec).
- Bulk (series/season) transcode quality — prerequisite for capped users to use series downloads
  and monitoring; lifts the `resolveBulkQuality` original-only rule.
- Profile-level download permission (parental-controls integration; downloads are already keyed by
  `profile_id`, so the data model is ready).
- Per-user managed-device count cap — revisit only if storage/quantity limits prove insufficient.
