# Autoscan: Polling Radarr/Sonarr to Trigger Targeted Library Scans

**Status:** Design approved, ready for implementation plan
**Date:** 2026-06-02
**Area:** new `internal/autoscan`, `internal/taskmanager/tasks`, `migrations/`, `internal/api`, `web/src/pages/AdminRequests.tsx`

## Summary

Silo currently learns about new media via scheduled/manual library scans. When
Radarr/Sonarr import a download, the new file only appears in Silo on the next
scan. This adds an **autoscan poller**: a periodic background task that polls the
configured Radarr/Sonarr instances for newly-imported files and enqueues
**targeted folder scans** into Silo's existing scan queue, so requested (and
non-requested) content shows up promptly.

The design follows the established autoscan pattern — poll arr import history,
translate the imported paths, and trigger targeted scans — but kept deliberately
simple. That pattern is normally built to orchestrate scans across many separate
media-server boxes, which needs a bounded fan-out guard and a retry queue. Silo
is a single service scanning *itself*, so the cross-node fan-out machinery is
unnecessary — imported paths become folder jobs in Silo's own worker-bounded
`scanqueue`.

## Goals

- Poll autoscan-enabled Radarr/Sonarr instances on an interval for import events.
- Translate arr import paths into Silo media folders and enqueue targeted scans.
- Reuse the Radarr/Sonarr instances already configured for requests
  (`request_integrations`) — no duplicate URLs/keys.
- Coalesce bursts (a season's episodes) into minimal redundant scans.
- Admin-configurable: global enable, interval, debounce, per-instance toggle,
  optional path rewrites, on-demand "poll now".

## Non-Goals

- No cross-node scan fan-out or bounded-concurrency semaphore (single service;
  the `scanqueue` workers are the bound).
- No separate retry queue (a failed source poll simply does not advance its
  high-water mark, so the next cycle retries the window).
- No arr webhook/"Connect" push integration (polling only, as requested; webhook
  push is a possible future add-on).
- No separate autoscan source credential store — sources are existing
  `request_integrations`.

## Background

- **`internal/scantrigger`** already maps a filesystem path to a Silo
  `MediaFolder` and scan path: `Resolver.Resolve(Request{Path}) -> Target{Folder,
  Mode, Path}` (via `LongestMatchingRoot` / `MatchFolderForPath`).
- **`internal/scanqueue`** executes scans through a worker-bounded queue:
  `Queuer.EnqueueScan(ctx, folderID, mode, path, trigger)` /
  `EnqueueScans(targets)`.
- **`internal/requests/arrclient`** is a thin Radarr/Sonarr HTTP client
  (`New(baseURL, apiKey, httpClient)`, `GetJSON`, `DoJSON`) — reused for the new
  history call.
- **`request_integrations`** holds each instance's `id`, `kind`, `base_url`, and
  Fernet-encrypted `api_key_ref`, resolved via the same secret resolver the
  request service uses.
- The **task manager** runs periodic tasks via the `taskmanager.Task` interface
  with interval `DefaultTriggers()` (e.g. `ReconcileRequestsTask`).

## Design

### 1. Data model & config

A new `internal/autoscan` package owns its own schema; `request_integrations`
stays focused on fulfillment.

**`autoscan_settings`** (singleton, mirrors `request_settings`):

| column | type / default | purpose |
|---|---|---|
| `id` | bool PK default true, singleton CHECK | |
| `enabled` | bool, default false | master on/off |
| `poll_interval_minutes` | int, default 10 | poll task interval |
| `debounce_seconds` | int, default 60 | folder re-scan suppression window |
| `created_at`, `updated_at` | timestamptz | |

**`autoscan_sources`** (one row per autoscan-enabled instance):

| column | type | purpose |
|---|---|---|
| `integration_id` | text PK → `request_integrations(id)` ON DELETE CASCADE | the source instance |
| `enabled` | bool, default false | per-instance autoscan toggle |
| `path_rewrites` | jsonb, default `[]` | `[{from, to}]` prefix overrides |
| `last_poll_at` | timestamptz, null | `history/since` high-water mark |
| `created_at`, `updated_at` | timestamptz | |

**Reused, not duplicated:** `base_url` / `api_key_ref` come from
`request_integrations`. Debounce/suppression state lives in **Redis**
(`autoscan:scanned:{folder_id}`, TTL = `debounce_seconds`), not Postgres.

### 2. Poll cycle

`autoscan.Service.PollOnce(ctx)` (invoked by the periodic task):

1. Load `autoscan_settings`; if `!enabled`, return immediately (no-op).
2. Load `autoscan_sources WHERE enabled`, joined to `request_integrations` for
   `kind` / `base_url` / `api_key_ref`.
3. For each source (sequential — a handful of instances):
   1. Decrypt the API key via the secret resolver.
   2. Record `cycleStart := now`.
   3. `arrclient.GetJSON("/api/v3/history/since?date={last_poll_at}&eventType=downloadFolderImported")`
      — both Radarr and Sonarr expose this. `last_poll_at` null → a sane floor
      (do not scan all history on first enable; floor at `cycleStart`).
   4. Extract the imported **file path** per event (Radarr: movie file path;
      Sonarr: episode file `outputPath`); take its **parent directory**.
   5. On success, advance that source's `last_poll_at = cycleStart`. On failure
      (arr unreachable, decode error), log and **do not** advance — next cycle
      retries the window.
4. For each extracted directory: apply path rewrites (§3), resolve via
   `scantrigger.Resolver` → `Target{Folder, Mode, Path}`. Unresolvable paths
   (outside Silo's media folders) are logged and skipped.
5. **Dedupe to unique folders** for this cycle; for each, check the Redis
   suppression key — if absent, `EnqueueScan(..., trigger="autoscan")` and set
   the key with TTL `debounce_seconds`; if present, skip (already scanned this
   window).

**Debounce rationale:** because the poller batches a whole interval of imports
per `history/since` call, within-cycle folder dedup does most of the work. The
Redis suppression key only prevents re-enqueuing the same folder on back-to-back
cycles. Silo's scanner is idempotent, so a season importing across two cycles
simply gets a second (cheap) scan that picks up the rest. This is simpler than a
"collect-expired" debounce plus a retry queue, which the polling + idempotent
re-scan model makes unnecessary.

### 3. Path resolution & scan enqueue

A small pipeline in front of the existing scan plumbing — no new scan logic:

1. **Rewrite** (per source, optional): apply the first matching `path_rewrites`
   entry as a prefix replacement (e.g. `/data/media -> /mnt/media`). No rewrites
   → path used as-is (shared-mount common case).
2. **Resolve** via `scantrigger.Resolver.Resolve(Request{Path: rewrittenDir})`
   → `Target{Folder, Mode, Path}`. No matching media folder → log + skip.
3. **Enqueue** deduped targets via `scanqueue` `EnqueueScans` with
   `trigger="autoscan"` (attributable in scan history, flows through the same
   worker-bounded queue).

### 4. Scheduling & concurrency

- **`internal/taskmanager/tasks/autoscan_poll.go`** implements `taskmanager.Task`
  (`Key: "autoscan_poll"`, name/description/category, `DefaultTriggers` = interval
  trigger seeded from `poll_interval_minutes`). Its run body calls
  `autoscan.Service.PollOnce(ctx)`.
- **Disabled = cheap no-op:** the task stays registered; `PollOnce` returns early
  when `enabled` is false. Toggling autoscan never (de)registers tasks.
- **Interval changes:** updating `poll_interval_minutes` reconfigures the task's
  interval trigger via the task manager's existing trigger-config path; if a live
  update is unavailable it applies on next start.
- **No overlap, no semaphore:** the task manager serializes a task's executions;
  sources are polled sequentially; scan execution is bounded by `scanqueue`. The
  single-service simplification: no bounded-fan-out semaphore is needed.
- **Primary/integrated server only** (same as the request reconcile task), not on
  standalone transcode/proxy worker nodes.

### 5. Admin API & UI

**API** (admin-gated, `/api/v1/admin/autoscan`, mirroring the request-settings
handlers; never echoes API keys):

- `GET/PUT /autoscan/settings` — `{enabled, poll_interval_minutes, debounce_seconds}`
- `GET /autoscan/sources` — per autoscan-eligible instance (joined from
  `request_integrations`): `{integration_id, name, kind, enabled, path_rewrites,
  last_poll_at}`
- `PUT /autoscan/sources/{integration_id}` — set `{enabled, path_rewrites}`
- `POST /autoscan/trigger` — run `PollOnce` once on demand; tight per-admin rate
  limit so it cannot hammer the arr instances
- `GET /autoscan/status` — `{enabled, per-source last_poll_at, last scan summary}`

**UI:** a new **"Autoscan" tab** in `web/src/pages/AdminRequests.tsx` (alongside
Queue / Settings / Integrations / User Overrides):

- Global controls: enable switch, poll interval, debounce window.
- Per-source list — one row per Radarr/Sonarr instance with an autoscan toggle,
  an optional `from → to` path-rewrite editor, and a read-only "last polled".
- A **"Poll now"** button → `POST /autoscan/trigger`.

Rate limits and admin-auth follow Silo's existing request-admin conventions.

### 6. Testing

- **Path rewrite:** prefix replacement, first-match ordering, no-match
  passthrough, trailing-slash handling.
- **arr history client:** table-driven `httptest` fixtures for both Radarr and
  Sonarr import-event JSON shapes; asserts the correct file path is extracted and
  its parent folder taken.
- **Dedupe + suppression:** many paths in one folder → one target; folder with a
  live suppression key skipped; absent/expired key enqueues.
- **`PollOnce` (integration seam):** fakes for source store, arr history client,
  `scantrigger.Resolver`, `scanqueue.Queuer`, Redis — asserts deduped folder
  scans enqueued with `trigger="autoscan"`, `last_poll_at` advanced on success,
  **not** advanced on per-source failure, clean no-op when disabled.
- **Unresolvable path:** logged + skipped, never enqueued or fatal.
- **Repository / migration:** `autoscan_settings` singleton + `autoscan_sources`
  CRUD and the `request_integrations` join; migration applies + rolls back
  (sidecar table, `ON DELETE CASCADE`).
- **Frontend:** Autoscan tab renders, per-source toggle / path-rewrite editor
  round-trips, "Poll now" calls the trigger endpoint.

## Risks & open considerations

- **First-enable floor:** `last_poll_at` null must floor at "now" (not epoch) so
  enabling autoscan doesn't enqueue a scan for every historical import.
- **history/since shape differences:** Radarr vs Sonarr import-event payloads
  differ; the path extraction is per-kind and covered by fixtures.
- **Path rewrites vs scantrigger roots:** if neither rewrites nor Silo's media
  folders match the arr path, the import is silently skipped (logged) — this is
  the expected "content outside Silo's libraries" behavior, but a misconfigured
  rewrite will look like "autoscan does nothing"; the `last_poll_at` + status
  endpoint and skip logs are the debugging surface.
- **Idempotent re-scan:** relies on Silo's scanner being safe to run repeatedly
  on a folder (it is — re-scan finds new files).

## References

- Existing Silo plumbing reused by this design: `internal/scantrigger/scantrigger.go`,
  `internal/scanqueue/`, `internal/requests/arrclient/`,
  `internal/taskmanager/tasks/reconcile_requests.go`
- Prior art: the general autoscan pattern (poll arr import history → translate
  paths → trigger targeted scans). Silo's variant drops the cross-node fan-out
  guard and retry queue, which only a multi-box orchestrator needs.
