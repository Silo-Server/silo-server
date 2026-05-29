# Trending Discover — Persistent Snapshot Design

Status: Approved (design)
Date: 2026-05-29

Commands assume the repository root is the cwd.

## Problem

The `trending_discover` home section pulls global trending (TMDB or Trakt),
matches it to titles in the viewer's libraries, and renders the result. Today
the external fetch + external-ID resolution are memoised in an **in-process
map** (`editorialCandidateCache`) keyed by `source|window|fetchLimit` with a
1-hour TTL and a `singleflight` group to collapse concurrent misses
(`internal/sections/fetcher.go`, `loadTrendingDiscoverContentIDs`).

That cache has three weaknesses, two of which we explicitly want to fix:

1. **No resilience to upstream failure.** At TTL expiry a request still blocks
   on the TMDB/Trakt call, and a slow or down provider degrades the home page.
   There is no concept of "serve the last good list."
2. **No observability.** Nothing records when trending last refreshed, whether
   it succeeded, or how many trending titles matched the catalog.
3. (Secondary) It is per-process and volatile — lost on restart, and each
   server instance fetches independently.

## Goals

- Reads of the trending section **never call the upstream provider** and never
  block on it. They serve a persisted, last-good list.
- A persisted, inspectable record with refresh history (the same "synced
  record" shape used by library collections).

## Non-goals

- Reusing the library-collection machinery. `library_collections.library_id` is
  `NOT NULL` (collections are library-scoped); trending is deliberately
  library-agnostic and appears once. Making collections library-agnostic is a
  large, separate change and is out of scope.
- An admin UI for the snapshot. The persisted columns make this possible later,
  but it is not part of this work.
- A shared/distributed cache (Redis). The DB is the single source of truth.

## Approach

Replace the lazy in-process cache with a **background-refreshed persistent
snapshot**. A scheduled task refreshes the trending list on an interval; the
read path only ever reads the persisted snapshot. This mirrors the
collection-sync *pattern* (`CollectionSyncScheduler` + `SyncCollectionsTask`)
without reusing its library-scoped storage.

The change cleanly separates **read** from **refresh**:

- The refresh path owns the upstream call and external-ID resolution.
- The read path owns only a primary-key snapshot lookup plus the existing
  per-viewer access filtering.

## Data model

New migration `167_trending_discover_snapshots`. (Originally numbered 166, but
the shared dev DB had already recorded version 166 from another branch's
`166_trending_blend_collection_type`; the migration runner dedupes by integer
version, so 166 was silently skipped. Renumbered to 167, the next free version.)

```
trending_discover_snapshots
  source          text         NOT NULL          -- 'tmdb' | 'trakt'
  window          text         NOT NULL          -- 'day' | 'week' (trakt canonicalized to 'week')
  content_ids     text[]       NOT NULL DEFAULT '{}'  -- ordered, resolved to library catalog, capped at 200
  entry_count     int          NOT NULL DEFAULT 0     -- raw provider entries fetched (matched-vs-trending observability)
  refreshed_at    timestamptz                     -- last successful refresh
  last_attempt_at timestamptz
  last_status     text                            -- 'ok' | 'empty' | 'error'
  last_error      text
  PRIMARY KEY (source, window)
```

Notes:

- One row per canonical `(source, window)` — at most a handful of rows.
- `content_ids` is catalog-matched but **viewer-agnostic**, exactly like the
  value the current cache holds. Per-viewer access filtering still happens at
  read time in `fetchItemsByContentIDs`, so a single row serves all viewers.
- The list is stored at the over-fetch cap (200). The read path truncates to the
  section's `ItemLimit`, decoupling the snapshot from per-section limits (the old
  `fetchLimit` cache-key dimension goes away).
- **Reliability invariant:** a failed refresh updates `last_attempt_at`,
  `last_status`, and `last_error` but **never clears `content_ids`**. The last
  good list keeps serving through an upstream outage.

The paired `.down.sql` drops the table.

## Components

All new server code lives in `internal/`.

### `TrendingSnapshotRepository` (new, `internal/sections/`)

Thin repository over the new table:

- `Get(ctx, source, window) (TrendingSnapshot, error)` — PK lookup.
- `Upsert(ctx, snapshot) error` — insert/update on `(source, window)`.
- `ListAll(ctx) ([]TrendingSnapshot, error)` — for future admin/inspection use
  and tests.

`Upsert` distinguishes a successful refresh (writes `content_ids`,
`refreshed_at`, `entry_count`, `last_status='ok'|'empty'`) from a failed one
(touches only `last_attempt_at`/`last_status='error'`/`last_error`, leaving
`content_ids` intact).

### `TrendingRefresher` (new, `internal/sections/trending_refresher.go`)

Owns the upstream call. Holds `TMDBTrending`, `TraktTrending`, `ItemRepo`, the
section repository (to enumerate used combos), and the snapshot repository. The
existing `fetchTrendingDiscoverEntries` and `resolveTrendingDiscoverIDs` logic
**moves here** from `fetcher.go`.

`RunOnce(ctx) (json.RawMessage, error)`:

1. Enumerate the distinct canonical `(source, window)` pairs from **enabled**
   `trending_discover` sections across all scopes (parse each section's
   `TrendingDiscoverParams`, apply the same source/window normalization the
   section uses, collapse Trakt to `week`).
2. For each pair: fetch the cap-200 list from the provider, resolve external IDs
   to library content IDs, and `Upsert`.
3. Return a JSON summary (e.g. `{combos, refreshed, empty, failed}`), mirroring
   `CollectionSyncResult`.

No used combos → no upstream calls (a dormant feature costs nothing). A
configured-but-empty provider yields `last_status='empty'` (no error noise). A
per-pair failure is recorded and does not abort the other pairs.

### `RefreshTrendingDiscoverTask` (new, `internal/taskmanager/tasks/`)

Mirrors `SyncCollectionsTask`: delegates to `TrendingRefresher.RunOnce` and
reports progress/result data. Default trigger: hourly interval (matches the
recipe's `DefaultCacheTTL` of 1h), plus a one-shot run at startup so the first
snapshot lands quickly rather than after a full interval.

### `Fetcher` (modified, `internal/sections/fetcher.go`)

- `loadTrendingDiscoverContentIDs` now reads the snapshot row via the snapshot
  repository and returns its `content_ids`. It drops the upstream fetch, the
  `singleflight` group, and the `editorialCandidateCache` usage **for trending**
  (the editorial-candidate cache remains for the editorial sections that still
  use it).
- `fetchTrendingDiscover` keeps its read-time responsibilities: access
  filtering, re-ordering to trending rank, truncation to `ItemLimit`. It no
  longer computes a `fetchLimit` for the read path.
- The `TMDBTrending`, `TraktTrending`, and trending-only use of `ItemRepo`
  migrate out of the read path into the refresher. The fetcher gains the
  snapshot repository dependency.

### Wiring (`cmd/silo/main.go`, `internal/api/router.go`)

- Construct `TrendingSnapshotRepository` and `TrendingRefresher` (wired with the
  TMDB/Trakt fetchers, `ItemRepo`, section repo, snapshot repo).
- Register `RefreshTrendingDiscoverTask` alongside the other task registrations.
- Hand the snapshot repository to the section `Fetcher`; move the upstream
  fetchers from the fetcher wiring to the refresher wiring.

## Data flow

**Refresh (background, hourly + startup):**
task → `Refresher.RunOnce` → enumerate distinct canonical `(source, window)`
from enabled `trending_discover` sections → per pair: fetch cap-200 → resolve
external IDs → `Upsert` snapshot.

**Read (per home render):**
`fetchTrendingDiscover` → `loadTrendingDiscoverContentIDs` reads the snapshot
row → `fetchItemsByContentIDs` applies the viewer's access filter → re-order to
trending rank → truncate to `ItemLimit`. No upstream call. A single PK lookup on
a ≤6-row table — negligible next to the queries `FetchAll` already runs, so no
in-process read cache is added (it would re-muddy the single-source-of-truth and
is premature).

## Edge behavior

- **Before first sync:** snapshot absent → section renders empty (no blocking,
  no upstream call). The startup task run closes this gap to roughly one fetch
  duration.
- **Provider unconfigured but a section exists:** `last_status='empty'`, no
  error.
- **Upstream failure on a pair that already has a snapshot:** prior
  `content_ids` keep serving; only the attempt/status/error columns update.
- **Trakt `day` vs `week`:** collapse to a single `week` row (Trakt ignores the
  window).

## Testing

- **Repository round-trip:** upsert then get; `content_ids` ordering preserved;
  success vs failure upsert paths.
- **Refresher `RunOnce`:** fake `TMDBTrending`/`TraktTrending`, fake section
  enumerator, in-memory `ItemRepo`. Assert ordered IDs upserted; `ok`/`empty`/
  `error` transitions; and the reliability invariant — a failed refresh
  preserves prior `content_ids`.
- **Read path:** inject nil upstream fetchers into the fetcher and assert the
  trending read still works from the snapshot (proves reads never call upstream).
- **Canonicalization:** Trakt `day` and `week` resolve to one row.

## Risks / follow-ups

- The data captured (`refreshed_at`, `last_status`, `last_error`, `entry_count`)
  enables a future admin/inspection surface; not built here.
- If multiple sections request very different `ItemLimit`s, all share the cap-200
  list and truncate — intended, and strictly more data than the old per-limit
  cache held.
```
