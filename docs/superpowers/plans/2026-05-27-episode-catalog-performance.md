# Episode Catalog Performance Plan

Commands assume the repository root is the cwd.

> **For agentic workers:** This is a discussion plan, not an execution script. Confirm the architecture decision points before implementing the durable catalog index work.

## Goal

Make `/api/v1/catalog?source=query&type=episode&library_id=<id>` fast enough for large series libraries and concurrent users. The first page of common episode-library browse requests should avoid full-library scans, repeated media-file aggregation, and exact counts unless the caller explicitly needs them.

Target behavior:

- First-page episode browse returns in less than 800 ms API time for common sorts/filters on a library with about 1 million episodes.
- Hot SQL paths avoid per-request aggregation over `media_files` or all `episode_libraries`.
- Exact totals are not computed by default for expensive query shapes.
- Existing API semantics continue to work for web, Android, Apple, and Jellyfin-compatible clients unless explicitly versioned.

## Current Findings

Testing used the large Series library endpoint shape:

```text
/api/v1/catalog?source=query&type=episode&library_id=2&limit=60&offset=0
```

Library 2 currently has about 767k episode-library rows. The recent fixes improved title and date-added browse, but several sorts and filters still spend too much time in SQL, especially when exact totals are requested.

### Sort timings

With exact totals enabled:

| Sort | API time |
| --- | ---: |
| `title asc` | 0.93s |
| `added_at desc` | 0.73s |
| `release_date desc` | 1.93s |
| `last_air_date desc` | 2.21s |
| `year desc` | 2.28s |
| `content_rating asc` | 1.21s |
| `runtime desc` | 1.97s |
| `rating_imdb desc` | 2.34s |
| `rating_tmdb desc` | 2.12s |
| `resolution desc` | 8.67s |
| `bitrate desc` | 5.53s |
| `progress desc` | 2.43s |
| `date_viewed desc` | 1.98s |
| `plays desc` | 2.06s |

With `include_total=false`:

| Sort | API time |
| --- | ---: |
| `title asc` | 0.39s |
| `added_at desc` | 0.29s |
| `release_date desc` | 1.50s |
| `last_air_date desc` | 1.84s |
| `year desc` | 1.45s |
| `runtime desc` | 1.55s |
| `rating_imdb desc` | 1.73s |
| `rating_tmdb desc` | 1.77s |
| `resolution desc` | 5.69s |
| `bitrate desc` | 5.09s |
| `date_viewed desc` | 2.59s |

The count split helped, but the page query itself is still too slow for many sort families.

### Filter timings

With `sort=title&order=asc` and exact totals:

| Filter | API time | Total |
| --- | ---: | ---: |
| none | 1.07s | 767k |
| `genre=Comedy` | 2.92s | 250k |
| `resolution=1080p` | 3.04s | 474k |
| `subtitle_language=en` | 6.57s | 524k |
| `dolby_vision=true` | 7.63s | 20k |
| `watched=true` | 16.32s | 11k |
| `watched=false` | 13.09s | 756k |
| `last_watched in_last 30d` | timed out at 30s | n/a |

With `include_total=false`, most of those page queries drop below 1s, except `last_watched in_last 30d`, which still times out. This confirms two independent problems:

- Exact totals are too expensive to run on every page 0 request.
- Some page plans start from the wrong side of the query and scan the whole library.

## Root Causes

1. The generic episode catalog path projects episodes into a `media_items`-shaped subquery and then asks one query builder to handle every sort/filter combination. This keeps code reusable, but it hides cheaper plans from PostgreSQL.

2. Technical sorts and filters aggregate `media_files` per request:

```text
media_files -> GROUP BY episode_id -> join all episode candidates -> sort
```

For `resolution` and `bitrate`, this means scanning and grouping hundreds of thousands of file rows before returning 60 items.

3. Personalized sorts and filters left-join small user-state sets onto the entire episode library. For `date_viewed desc`, `plays desc`, and `last_watched`, the database sorts mostly-null rows from the whole library instead of starting with the few watched rows.

4. Exact counts use the same broad filtered relation as the page query. This is acceptable for small result sets, but expensive for common filters like `watched=false`, `subtitle_language=en`, or genre filters that match hundreds of thousands of rows.

5. Series-level episode filters and sorts duplicate series metadata across all episodes. A filter like `genre=Comedy` is logically a series filter, but the current episode projection evaluates it at episode scale.

## Prototype Results

The following SQL prototypes were tested against the same data shape to validate that the proposed plan is viable.

### User-state-first plans

Starting from watched/progress rows, then joining to episode library membership:

| Query shape | SQL time |
| --- | ---: |
| `last_watched in_last 30d` | about 5 ms |
| `date_viewed desc` first page | about 282 ms |
| `watched=true` title page | about 261 ms |
| `watched=true` exact count | about 80 ms |

This proves the `last_watched` timeout is a planner/source problem, not an unavoidable data-size problem.

### Precomputed technical stats

A temporary per-episode/per-library stats table was built with max resolution rank, max bitrate, HDR/Dolby Vision flags, and audio/subtitle language arrays. Build time was about 22s as a one-time backfill over the dev data; scanner maintenance would keep the permanent table updated incrementally.

Using that temporary stats table:

| Query shape | SQL time |
| --- | ---: |
| `subtitle_language=en` filter page | about 24 ms |
| `dolby_vision=true` filter page | about 128 ms |
| `bitrate desc` sort page | about 1 ms |
| `resolution desc` sort page | about 365 ms |

This validates a durable browse index or stats table for technical fields.

## Architecture Decision

There are two viable paths.

### Option A: Incremental Specialized Plans

Add specific fast paths for technical stats and user-state filters while keeping the generic query builder as the main executor.

Pros:

- Lower implementation cost.
- Smaller schema change.
- Directly fixes the worst outliers: `resolution`, `bitrate`, `subtitle_language`, `dolby_vision`, `last_watched`, `watched`.

Cons:

- Leaves several episode metadata sorts around 1.5-2s.
- Keeps exact-count complexity spread through the generic executor.
- Each new slow query shape becomes another special case.

### Option B: Durable Episode Browse Index

Create a persistent per-library episode browse index table that stores the sort/filter keys needed to find page IDs quickly, then hydrate only the selected page rows from `episodes` and parent `media_items`.

This is the recommended target if the server needs to handle hundreds of concurrent users. It turns page selection into indexed top-N scans over a narrow table and avoids repeated joins/aggregates over broad catalog tables.

Proposed table shape:

```sql
CREATE TABLE episode_catalog_entries (
    media_folder_id integer NOT NULL,
    episode_id text NOT NULL,
    series_id text NOT NULL,
    sort_key text NOT NULL,
    added_at timestamptz NOT NULL,
    episode_air_date date,
    year integer NOT NULL,
    genres text[] NOT NULL,
    studios text[] NOT NULL,
    networks text[] NOT NULL,
    countries text[] NOT NULL,
    original_language text NOT NULL,
    content_rating text NOT NULL,
    content_rating_rank integer NOT NULL,
    status text NOT NULL,
    runtime integer NOT NULL,
    rating_imdb numeric,
    rating_tmdb numeric,
    max_resolution_rank integer,
    max_bitrate integer,
    has_hdr boolean NOT NULL DEFAULT false,
    has_dolby_vision boolean NOT NULL DEFAULT false,
    audio_language_codes text[] NOT NULL DEFAULT '{}',
    subtitle_language_codes text[] NOT NULL DEFAULT '{}',
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (media_folder_id, episode_id)
);
```

The table should not become a second full metadata store unless measurements prove that hydration is too expensive. The first implementation can use it to pick ordered `episode_id` values, then join only those 60 IDs back to the existing episode projection for response shaping.

Recommended indexes:

```sql
CREATE INDEX idx_episode_catalog_entries_title
ON episode_catalog_entries (media_folder_id, sort_key, episode_id);

CREATE INDEX idx_episode_catalog_entries_added
ON episode_catalog_entries (media_folder_id, added_at DESC, sort_key, episode_id);

CREATE INDEX idx_episode_catalog_entries_air_date
ON episode_catalog_entries (media_folder_id, episode_air_date DESC NULLS LAST, sort_key, episode_id);

CREATE INDEX idx_episode_catalog_entries_year
ON episode_catalog_entries (media_folder_id, year DESC, sort_key, episode_id);

CREATE INDEX idx_episode_catalog_entries_runtime
ON episode_catalog_entries (media_folder_id, runtime DESC NULLS LAST, sort_key, episode_id);

CREATE INDEX idx_episode_catalog_entries_imdb
ON episode_catalog_entries (media_folder_id, rating_imdb DESC NULLS LAST, sort_key, episode_id);

CREATE INDEX idx_episode_catalog_entries_tmdb
ON episode_catalog_entries (media_folder_id, rating_tmdb DESC NULLS LAST, sort_key, episode_id);

CREATE INDEX idx_episode_catalog_entries_resolution
ON episode_catalog_entries (media_folder_id, max_resolution_rank DESC NULLS LAST, sort_key, episode_id);

CREATE INDEX idx_episode_catalog_entries_bitrate
ON episode_catalog_entries (media_folder_id, max_bitrate DESC NULLS LAST, sort_key, episode_id);

CREATE INDEX idx_episode_catalog_entries_hdr
ON episode_catalog_entries (media_folder_id, sort_key, episode_id)
WHERE has_hdr;

CREATE INDEX idx_episode_catalog_entries_dolby_vision
ON episode_catalog_entries (media_folder_id, sort_key, episode_id)
WHERE has_dolby_vision;
```

Evaluate either `btree_gin` multi-column GIN or separate GIN indexes for array filters:

```sql
CREATE INDEX idx_episode_catalog_entries_genres_gin
ON episode_catalog_entries USING gin (genres);

CREATE INDEX idx_episode_catalog_entries_audio_gin
ON episode_catalog_entries USING gin (audio_language_codes);

CREATE INDEX idx_episode_catalog_entries_subtitle_gin
ON episode_catalog_entries USING gin (subtitle_language_codes);
```

## Query Plan Design

Introduce a planner layer between `CatalogResolver` and `QueryExecutor`.

```go
type CatalogPlan struct {
    PageSQL          string
    PageArgs         []any
    CountSQL         string
    CountArgs        []any
    CountMode        CatalogCountMode
    SnapshotStrategy SnapshotStrategy
    PlanName         string
}
```

The planner chooses one of these sources:

- `episode_catalog_entries` for normal episode library browse.
- `episode_user_state` CTE/source for watched/date-viewed/plays/last-watched shapes.
- Existing generic `QueryExecutor` fallback for unsupported combinations.

The page query should use a narrow ID-first CTE:

```sql
WITH page AS (
    SELECT ece.episode_id, ece.sort_key
    FROM episode_catalog_entries ece
    WHERE ece.media_folder_id = $1
    ORDER BY ece.sort_key ASC, ece.episode_id ASC
    LIMIT $2 OFFSET $3
)
SELECT ...
FROM page
JOIN episodes e ON e.content_id = page.episode_id
JOIN media_items si ON si.content_id = e.series_id
ORDER BY page.sort_key ASC, page.episode_id ASC;
```

Each concrete sort should carry the selected sort keys through the `page` CTE and use the same order in the final hydrated SELECT. Do not compute `row_number()` over the full candidate set just to preserve order; that would reintroduce a broad sort before `LIMIT`.

For sort/filter shapes that can be satisfied entirely from `episode_catalog_entries`, counts become simple index-backed counts on the narrow table. For expensive or broad counts, the planner should return `total_exact=false` unless exact totals are explicitly requested.

## User-State Plan

Personalized sort/filter logic should start from user state when the requested result set is mostly watched/progress rows.

Fast paths:

- `last_watched` comparisons and `in_last`.
- `watched=true`.
- `in_progress=true`.
- `favorited=true`.
- `in_watchlist=true`.
- `date_viewed desc` first segment.
- `plays desc` first segment.
- `progress desc` first segment.

The user-state source can start as request-time CTEs over `user_watch_history`, `user_watch_progress`, `user_favorites`, and `user_watchlist`. If concurrent load tests show those CTEs are still too expensive for heavy users, promote them to a maintained `profile_media_state` aggregate table.

Special handling:

- `watched=false` should not scan user state first because the result set is usually almost the whole library. Use the episode browse index with an anti-join against the small watched set, and compute exact count as `library_count - watched_count` when possible.
- `date_viewed desc` with deep offsets eventually reaches the unviewed segment. First implement the watched segment fast path and fall back only when the requested offset exceeds the watched count.
- `last_watched lt/lte` includes never-watched rows because the current SQL uses `-infinity`. Keep this behavior, but route `gt/gte/between/in_last` through user-state-first plans.

## Count Strategy

Exact totals are the largest remaining source of avoidable database load. The UI already understands `total_exact=false` and can estimate virtualized height.

Plan:

1. Change `LibraryBrowse` to call `useCatalogWindow(..., includeTotal: false)` for the first page unless a specific UI state truly needs an exact count.
2. Add backend count modes:
   - `none`: return `total_exact=false` and one extra row for `has_more`.
   - `fast`: exact count from a narrow indexed table or small user-state source.
   - `cached`: count reused from a query hash and invalidated by scanner/user-state writes.
   - `exact`: full exact count, only when requested.
3. For plain episode library counts, use `episode_catalog_entries` or `episode_libraries` directly.
4. For watched filters:
   - `watched=true`: count from user-state source joined to library membership.
   - `watched=false`: `library_total - watched_true_count` when the filter set permits it.
5. For technical filters, count from `episode_catalog_entries`.

Do not run broad exact counts by default under web browse traffic. That path does not scale to hundreds of users.

## Implementation Phases

### Phase 0: Observability and Benchmark Harness

- Add structured slow-query logging around catalog page and count execution:
  - `source`
  - `media_scope`
  - `library_count`
  - `sort`
  - `filter_count`
  - `include_total`
  - `plan_name`
  - page SQL duration
  - count SQL duration
- Add a local benchmark helper under `scripts/` that exercises the sort/filter matrix without embedding credentials.
- Add a small `EXPLAIN (ANALYZE, BUFFERS)` note template for comparing plans.

Verification:

```bash
go test ./internal/catalog -count=1
```

### Phase 1: Stop Exact Counts by Default in Library Browse

- Change `web/src/pages/LibraryBrowse.tsx` to pass `includeTotal: false`.
- Keep `CatalogFiltersPanel` result count display compatible with estimated totals or suppress exact count text when `total_exact=false`.
- Confirm `ItemGrid` still virtualizes correctly using the existing estimated-total logic in `useCatalogWindow`.

Verification:

```bash
cd web && pnpm run lint
```

### Phase 2: Durable Episode Browse Index

- Add migrations for `episode_catalog_entries`.
- Backfill from:
  - `episode_libraries`
  - `episodes`
  - parent series rows in `media_items`
  - active `media_files`
- Add a repository/service that can refresh entries for:
  - one episode
  - one series
  - one library
  - one changed media file
- Wire refresh calls into scanner and metadata writes where episode visibility or sort/filter keys change.
- Keep the existing `episode_libraries` table as the source of truth for membership; the new table is a maintained read model.

Verification:

```bash
go test ./internal/catalog ./internal/scanner -count=1
```

### Phase 3: Episode Catalog Planner

- Add a planner that recognizes episode library browse requests and emits ID-first page SQL against `episode_catalog_entries`.
- Keep the existing `QueryExecutor` as fallback.
- Support these first:
  - no filters, all common sorts
  - `genre`, `year`, `content_rating`, `status`
  - `resolution`, `bitrate`, `audio_language`, `subtitle_language`, `hdr`, `dolby_vision`
- Add query-builder tests that assert the selected plan name and SQL shape for each supported sort/filter family.

Verification:

```bash
go test ./internal/catalog -count=1
```

### Phase 4: User-State Planner

- Add user-state-first plans for `last_watched`, `watched=true`, `in_progress=true`, `date_viewed desc`, `plays desc`, and `progress desc`.
- Add `watched=false` fast count using complement logic where safe.
- Preserve current hidden-history behavior.
- Preserve current semantics for never-watched rows on `last_watched lt/lte`.

Verification:

```bash
go test ./internal/catalog ./internal/userstore -count=1
```

### Phase 5: Load Testing and Tuning

- Deploy to dev.
- Re-run the sort/filter matrix with exact totals disabled and enabled.
- Run concurrent load against the high-traffic shapes:
  - `title asc`
  - `added_at desc`
  - `release_date desc`
  - `resolution desc`
  - `bitrate desc`
  - `subtitle_language=en`
  - `dolby_vision=true`
  - `date_viewed desc`
  - `last_watched in_last 30d`
- Use PostgreSQL query plans and slow-query logs to tune indexes before considering the work complete.

Target load result:

- 50 concurrent catalog requests: p95 under 1s for indexed paths.
- No request over 5s for supported sort/filter shapes.
- No supported first-page request performs a full `media_files` aggregate.

## Testing Matrix

The benchmark harness should cover every executable sort and filter family.

Sorts:

- `title`
- `added_at`
- `release_date`
- `last_air_date`
- `year`
- `content_rating`
- `runtime`
- `rating_imdb`
- `rating_tmdb`
- `rating_rt_critic`
- `rating_rt_audience`
- `resolution`
- `bitrate`
- `progress`
- `date_viewed`
- `plays`

Filters:

- `type`
- `genre`
- `year`
- `rating_imdb`
- `studio`
- `network`
- `country`
- `original_language`
- `content_rating`
- `added_at`
- `release_date`
- `status`
- `actor`
- `director`
- `writer`
- `producer`
- `watched`
- `favorited`
- `in_watchlist`
- `in_progress`
- `last_watched`
- `resolution`
- `hdr`
- `dolby_vision`
- `bitrate`
- `audio_language`
- `subtitle_language`

For each case, capture:

- API time with exact totals.
- API time with `include_total=false`.
- page SQL time.
- count SQL time.
- rows scanned/aggregated from `EXPLAIN`.
- whether the planner used `episode_catalog_entries`, user state, or fallback.

## Risks and Open Questions

- The browse index is a read model. The source of truth stays in `episode_libraries`, `episodes`, `media_items`, and `media_files`, so refresh paths must be reliable and observable.
- Scanner and metadata updates can touch large series. Batch refreshes should be chunked and idempotent.
- Multi-library browse needs clear semantics for `added_at` and technical stats. The current single-library Series Library case should be optimized first.
- Exact snapshot semantics may conflict with cached counts. Page snapshots should remain stable; counts can be marked non-exact unless they come from the same snapshot-safe path.
- API response changes around count modes may require Android and Apple follow-up. Keeping the existing `total_exact=false` behavior avoids most client churn.
- Person filters may need fallback initially unless episode-level people data is available and indexed.
- Array GIN filters should be checked with real plans. If separate GIN indexes do too much post-filtering by library, use `btree_gin` multi-column indexes or add selective partial indexes for common libraries.

## Recommended Decision

Proceed with Option B in phases. The prototype timings show that denormalizing technical stats and starting user-state queries from user tables both work. A durable `episode_catalog_entries` read model generalizes those wins to normal episode metadata sorts too, which is the more scalable path for hundreds of users.

Use Phase 1 as the immediate load reducer, then implement the durable browse index and planner behind the existing catalog API. Keep the generic executor as a fallback until the measured matrix shows the new planner covers the important sort/filter combinations.
