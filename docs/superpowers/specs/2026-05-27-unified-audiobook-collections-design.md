# Unified User Collections (Audiobooks + Movies + TV) Design

**Status:** design — pending implementation plan.

**Source brainstorm:** in-session, 2026-05-27. Driven by audiobook coverage gaps in `page_sections`, `library_collections`, and `user_personal_collections`, plus a parallel `abs_*` collection/playlist/smart-collection stack that doesn't intersect with the silo-canonical tables.

**Commands assume the repository root is the cwd.**

---

## 1. Problem

Audiobooks today live in a parallel collections world from movies and TV:

| Surface | Audiobook coverage today |
|---|---|
| `page_sections` (homepage rails) | None. Recipes reference only `movie`/`series`. |
| `library_collections` (admin-owned, library-scoped) | Schema supports `MediaAudiobook` kind but nothing surfaces audiobook-typed admin collections in practice. |
| `user_personal_collections` (user-owned) | Zero references to audiobooks anywhere in `internal/usercollections/`. |
| `abs_user_collections` + `abs_collection_items` | Full audiobook-only user-curated collections behind the ABS-compat API. |
| `abs_smart_collections` | Audiobook-only smart collections with a rule DSL (port of continuum-plugin-audiobooks). |
| `abs_playlists` + `abs_playlist_items` | Audiobook + podcast-episode ordered playlists. |

Net effect: audiobooks don't appear in any of silo's first-party discovery surfaces, the ABS-app's collection/playlist features don't intersect with silo's native ones, and we maintain two parallel storage stacks.

The data inside the `abs_*` tables today is essentially empty (1 playlist, 0 user collections, 0 smart collections), so unification can land via hard cutover with minimal data migration cost.

---

## 2. Goals

- One canonical storage for user-owned lists across all media types.
- Audiobook items eligible for `page_sections` recipes, `library_collections`, and `user_personal_collections`.
- ABS-compat endpoints (`/api/collections`, `/api/playlists`, `/api/smart-collections`) continue to work and return identical wire shapes, but their persistence collapses into the canonical store.
- One smart-collection engine evaluated against all media types.
- Podcast-episode-level granularity preserved (ABS playlists can name a specific episode within a library item).

## 3. Non-goals

- Admin-curated audiobook collections via the ABS API surface. `library_collections` already supports audiobooks; whether ABS clients should see those admin lists is a separate question deferred for a later design.
- Frontend changes. Audiobook detail/library pages already exist (per `2026-05-24-audiobook-ui-redesign`) and will consume the unified endpoints when they ship. An admin section-builder media-type selector and any UI surfacing of audiobook sections/collections are follow-up plans.
- Cross-user shared collections, public sharing, social features. Out of scope.
- Episode-level granularity for movies/TV. The new `sub_item_id` column is nullable and only populated by podcast playlists.

## 4. Architecture

### 4.1 Storage model

One canonical user-owned-lists table — `user_personal_collections` — extended to discriminate four flavors via `collection_type`:

| `collection_type` | Semantics |
|---|---|
| `manual` | Existing. Hand-curated list of items. Order optional. |
| `synced` | Existing. External sync (Trakt etc.). Items derived from external source. |
| `playlist` | **New.** Ordered sequence; `position` column on items is meaningful. |
| `smart` | **New.** Rule-based; items materialized at read time from `query_definition` via `internal/smartcoll/`. |

`user_personal_collection_items` gains one nullable column:

```sql
ALTER TABLE user_personal_collection_items ADD COLUMN sub_item_id text NOT NULL DEFAULT '';
```

`sub_item_id` is populated only when the entry refers to a sub-item of a library item (today: podcast episode within a podcast library item). All other entries leave it empty. Audiobook entries always use empty `sub_item_id` because an audiobook is a single `media_items` row.

The CHECK constraint on `collection_type` is widened to admit the new values:

```sql
ALTER TABLE user_personal_collections
  DROP CONSTRAINT IF EXISTS user_personal_collections_type_check;
ALTER TABLE user_personal_collections
  ADD CONSTRAINT user_personal_collections_type_check
  CHECK (collection_type IN ('manual', 'synced', 'playlist', 'smart'));
```

Admin-curated lists (`library_collections`, `library_collection_items`, etc.) are unchanged. They already support audiobooks through the existing `MediaKind` enum.

### 4.2 Smart-collection engine

The current audiobook-only smart-collection engine in `internal/audiobooks/smartcoll/` is promoted to `internal/smartcoll/`:

```
internal/audiobooks/smartcoll/  →  internal/smartcoll/
```

Rationale: smart collections are no longer audiobook-only. The DSL operates on `media_items` regardless of type; audiobook-specific rule kinds (e.g. `narrator`, `series_position`) stay registered but evaluate to no-op predicates on non-audiobook items.

The existing simple-shape `query_definition` on `user_personal_collections` (mostly `library_ids` filter) is a strict subset of the smartcoll DSL — existing rows continue to evaluate correctly because their library-id filter maps to the new DSL's library-id predicate.

### 4.3 ABS-compat handler mapping

The three ABS store adapter files in `internal/audiobooks/` get their bodies rewritten to query the canonical tables. Method signatures stay the same so the ABS HTTP handlers above them don't change.

| ABS endpoint | Backing query after cutover |
|---|---|
| `GET /api/libraries/{id}/collections` | `SELECT … FROM user_personal_collections WHERE collection_type='manual'` joined to items in this library |
| `POST /api/collections` | INSERT `user_personal_collections` with `collection_type='manual'`, scoping via `query_definition.library_ids` |
| `GET /api/libraries/{id}/playlists` | `SELECT … FROM user_personal_collections WHERE collection_type='playlist'` joined to items in this library |
| `POST /api/playlists` | INSERT `user_personal_collections` with `collection_type='playlist'` |
| `GET /api/libraries/{id}/smart-collections` | `SELECT … FROM user_personal_collections WHERE collection_type='smart'`; items materialized via `internal/smartcoll` |
| `episode_id` in playlist items wire shape | maps to `sub_item_id` column |

### 4.4 Page sections (homepage rails)

`page_sections` rows gain a media-types filter. The cleanest carrier is a typed column:

```sql
ALTER TABLE page_sections
  ADD COLUMN media_types text[] NOT NULL DEFAULT ARRAY['movie','series'];
```

Default preserves the current "movies and TV" behavior on existing rows so the migration doesn't change any user-visible rails.

Each recipe declaration in `internal/sections/recipes/` gains a `SupportedMediaTypes` field. Fetchers add `WHERE mi.type = ANY($media_types)` to their queries. Existing recipes set `SupportedMediaTypes = ['movie','series']`; they keep their current behavior. Recipes that work for audiobooks declare `['movie','series','audiobook']` (or `['audiobook']` for audiobook-only ones).

Two new recipes ship at the same time:

- **`continue_listening`** — analog of `continue_watching`; audiobook items with non-zero progress and not finished.
- **`by_audiobook_series`** — book series rail, drawing from the `audiobook_series` table; mirrors `by_show`.

More audiobook-flavored recipes (top narrators, by genre, new from followed authors) are deferred until the baseline two are exercised.

### 4.5 ABS app contract preservation

Wire-shape compatibility is part of the contract — the ABS Android/iOS apps cannot break. The store-adapter rewrites must produce byte-for-byte identical JSON for the existing endpoints. Concretely:

- IDs remain stable for the moved row(s). The migration preserves `abs_playlists.id` as the new `user_personal_collections.id`.
- `episode_id` field is emitted from `sub_item_id` (empty string when null/empty).
- `is_public` field on playlists/smart collections is emitted from `is_shared`.
- `cover_item` on playlists — needs decision (see §6).

### 4.6 Module structure after the change

```
internal/
  smartcoll/                    -- NEW (promoted from audiobooks/smartcoll/)
    types.go
    evaluator.go
    rule_registry.go
  usercollections/              -- existing; extended for playlist/smart kinds
    listing.go                  -- gains Kind filter
    types.go                    -- adds CollectionKind = playlist|smart
    smartfetch.go               -- new; materializes smart-collection items via internal/smartcoll
  sections/
    recipes/
      *.go                      -- each gains SupportedMediaTypes
      continue_listening.go     -- new
      by_audiobook_series.go    -- new
  audiobooks/
    abs_collection_store.go     -- rewritten: canonical SQL
    abs_playlist_store.go       -- rewritten: canonical SQL
    abs_smart_collection_store.go -- rewritten: canonical SQL + smartcoll
    smartcoll/                  -- REMOVED (lifted to internal/smartcoll/)
```

---

## 5. Migration

Single pair: `migrations/156_unify_user_collections.up.sql` / `.down.sql`. Hard cutover.

### 5.1 Up

```sql
-- 1. Extend canonical items table with sub-item granularity.
ALTER TABLE user_personal_collection_items
  ADD COLUMN sub_item_id text NOT NULL DEFAULT '';

-- 2. Widen the collection_type enum.
ALTER TABLE user_personal_collections
  DROP CONSTRAINT IF EXISTS user_personal_collections_type_check;
ALTER TABLE user_personal_collections
  ADD CONSTRAINT user_personal_collections_type_check
  CHECK (collection_type IN ('manual', 'synced', 'playlist', 'smart'));

-- 3. Move the single existing abs_playlists row.
INSERT INTO user_personal_collections
  (id, user_id, profile_id, name, description, collection_type,
   is_shared, created_at, updated_at, creator_profile_id)
SELECT
  id, user_id, COALESCE(profile_id::text, ''), name, description, 'playlist',
  is_public, created_at, updated_at, COALESCE(profile_id::text, '')
FROM abs_playlists;

INSERT INTO user_personal_collection_items
  (user_id, collection_id, media_item_id, sub_item_id, position, added_at)
SELECT p.user_id, i.playlist_id, i.library_item_id, i.episode_id, i.position, i.added_at
FROM abs_playlist_items i
JOIN abs_playlists p ON p.id = i.playlist_id;

-- 4. Drop the abs_* collection tables.
DROP TABLE abs_playlist_items;
DROP TABLE abs_playlists;
DROP TABLE abs_collection_items;
DROP TABLE abs_user_collections;
DROP TABLE abs_smart_collections;

-- 5. Extend page_sections with media-type filter.
ALTER TABLE page_sections
  ADD COLUMN media_types text[] NOT NULL DEFAULT ARRAY['movie','series'];
```

### 5.2 Down

```sql
-- Reverse the page_sections column.
ALTER TABLE page_sections DROP COLUMN media_types;

-- Recreate the abs_* tables empty by re-applying their CREATE TABLE
-- statements verbatim from the original migration files (no data restored):
--   migrations/149_abs_user_collections.up.sql   → abs_user_collections
--   migrations/150_abs_collection_items.up.sql   → abs_collection_items
--   migrations/151_abs_playlists.up.sql          → abs_playlists
--   migrations/152_abs_playlist_items.up.sql     → abs_playlist_items
--   migrations/153_abs_smart_collections.up.sql  → abs_smart_collections
-- The implementer copies the CREATE TABLE bodies (and indexes) into this
-- down migration; constraint/index names must match the originals so a
-- subsequent up-down-up cycle is idempotent.

-- Remove rows we promoted.
DELETE FROM user_personal_collection_items
  WHERE collection_id IN (
    SELECT id FROM user_personal_collections WHERE collection_type IN ('playlist','smart')
  );
DELETE FROM user_personal_collections WHERE collection_type IN ('playlist','smart');

-- Restore narrow CHECK constraint.
ALTER TABLE user_personal_collections
  DROP CONSTRAINT IF EXISTS user_personal_collections_type_check;
ALTER TABLE user_personal_collections
  ADD CONSTRAINT user_personal_collections_type_check
  CHECK (collection_type IN ('manual', 'synced'));

-- Drop the sub_item_id column.
ALTER TABLE user_personal_collection_items DROP COLUMN sub_item_id;
```

The down migration is symmetrically structured but lossy in reverse: rolling back loses any playlist/smart rows created after the cutover. That's acceptable because the abs_* tables were near-empty before the up.

### 5.3 Risk

| Risk | Probability | Mitigation |
|---|---|---|
| Down-migration loses real user data | Low (one row today; grows over time) | Snapshot the table before running down in any prod context |
| ABS app sees wire-shape regressions | Medium | Snapshot tests on JSON output of each ABS endpoint before/after |
| smartcoll DSL gap (silo-native query_definition row that doesn't fit DSL subset) | Low | Pre-migration query enumerates non-conforming rows; spec asserts subset compat |
| `library_collections` UI gains audiobook rows it can't render | Low | UI changes deferred; admin doesn't currently create audiobook-typed library collections |

---

## 6. Open questions

1. **Playlist `cover_item` field.** `abs_playlists.cover_item` is a FK to `media_items.content_id`. `user_personal_collections.poster_url` is a plain text URL. Two options for the migration: (a) drop `cover_item`, regenerate poster URLs from the first item in the playlist; or (b) add a `cover_content_id` column to `user_personal_collections`. Recommend (a) for simplicity — the existing playlist row has no `cover_item` set.

2. **`is_pinned` on smart collections.** `abs_smart_collections.is_pinned` has no analog in `user_personal_collections`. Likely needs a new boolean column; defer until smart-collection UI work names a use case.

3. **`color` on smart collections.** `abs_smart_collections.color` has no analog. Same call as `is_pinned` — defer; the existing 0 rows means no data to preserve.

4. **Admin-curated audiobook collections in the ABS API.** Out of scope per §3, but flagged for a future design.

---

## 7. Testing

- **Migration round-trip test.** Run `up.sql` → `down.sql` → `up.sql` on a fresh test DB seeded with one of each `abs_*` row. Assert canonical-table contents are identical before and after.
- **ABS endpoint wire-shape snapshots.** Generate JSON for `GET /api/libraries/{id}/collections|playlists|smart-collections` before the cutover; assert byte-equal after.
- **smartcoll engine portability.** Move the existing `internal/audiobooks/smartcoll/*_test.go` into `internal/smartcoll/` and add three new tests:
  - audiobook-specific rule (`narrator` filter) evaluated against an audiobook → expected items.
  - audiobook-specific rule evaluated against a movie → empty set (no-op predicate).
  - simple library-id filter (silo-native query_definition shape) evaluated under the unified DSL → expected items.
- **Section recipe regression.** Existing recipes with default `media_types=['movie','series']` return identical results to today. New `continue_listening` and `by_audiobook_series` recipes covered with focused unit tests.

---

## 8. Sub-projects

This design is decomposed for implementation:

1. **Schema migration + canonical storage extensions** — migration 156, CHECK/column additions, no behavior changes yet.
2. **Smart-collection engine lift** — move `internal/audiobooks/smartcoll/` to `internal/smartcoll/`, update imports, no logic changes.
3. **ABS store adapter rewrites** — collection/playlist/smart-collection stores query canonical tables.
4. **Section recipes for audiobooks** — recipe `SupportedMediaTypes` field, two new recipes, fetcher type filter.
5. **Documentation + follow-up tickets** — open-question call-outs (cover_item, is_pinned, color, admin collections in ABS).

Each sub-project has a single-MR scope and ships independently of the others. Sub-project 1 lands first; sub-projects 2–4 can land in any order after it.

---

## 9. Source references

- Existing canonical tables: `internal/usercollections/`, `internal/collections/templates/templates.go`
- Existing smart-collection engine: `internal/audiobooks/smartcoll/`
- Existing section recipes: `internal/sections/recipes/`, `internal/api/handlers/sections_preview.go`, `internal/api/handlers/sections_bulk.go`, `internal/api/handlers/recipes.go`
- Existing ABS store adapters: `internal/audiobooks/abs_collection_store.go`, `internal/audiobooks/abs_playlist_store.go`, `internal/audiobooks/abs_smart_collection_store.go`
- Related shipped specs: `docs/superpowers/specs/2026-05-26-abs-smart-collections-design.md`, `docs/superpowers/specs/2026-05-24-audiobooks-absorption-design.md`
