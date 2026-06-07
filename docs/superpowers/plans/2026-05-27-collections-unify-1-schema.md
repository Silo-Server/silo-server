# Collections Unification — Sub-project 1: Schema Migration

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land migration 156 — extend `user_personal_collections` to admit `'playlist'` and `'smart'` types, add nullable `sub_item_id` to its items table, add `media_types` filter to `page_sections`, move the single existing `abs_playlists` row into the canonical store, and drop the five `abs_*` collection tables.

**Architecture:** Pure schema migration + one-row data move. No application code changes. The follow-up sub-projects 2–4 wire silo to the new schema; this one is just the storage foundation. Hard cutover — the `abs_*` tables are near-empty (1 playlist row, 0 of everything else) so rollback risk is minimal.

**Tech Stack:** PostgreSQL 18, raw SQL migrations under `migrations/`, no Go code in this sub-project.

**Commands assume the repository root is the cwd.**

**Source spec:** `docs/superpowers/specs/2026-05-27-unified-audiobook-collections-design.md` §4.1, §4.4, §5.

---

## File map

**Create:**
- `migrations/156_unify_user_collections.up.sql`
- `migrations/156_unify_user_collections.down.sql`

**No Go code modified in this sub-project.** Application code keeps querying the (now-dropped) `abs_*` tables until sub-projects 3 and 4 land, which means **silo will break in dev/staging until sub-project 3 ships.** Land this sub-project and sub-project 3 together (single MR) if a production deploy is imminent. Otherwise it's safe to ship in isolation on a feature branch that hasn't been merged yet.

---

## Task 1: Up-migration body

**Files:**
- Create: `migrations/156_unify_user_collections.up.sql`

- [ ] **Step 1: Write the migration file**

```sql
-- Unify user-owned lists into user_personal_collections.
--
-- See docs/superpowers/specs/2026-05-27-unified-audiobook-collections-design.md
-- for design rationale.

-- 1. Sub-item granularity column on the canonical items table.
--    Empty string for whole-item entries; populated for podcast-episode
--    playlist entries (sub_item_id == abs_playlist_items.episode_id).
ALTER TABLE user_personal_collection_items
    ADD COLUMN sub_item_id text NOT NULL DEFAULT '';

-- 2. Widen the collection_type domain. The pre-existing CHECK (if any)
--    only admits 'manual' and 'synced'.
ALTER TABLE user_personal_collections
    DROP CONSTRAINT IF EXISTS user_personal_collections_type_check;
ALTER TABLE user_personal_collections
    ADD CONSTRAINT user_personal_collections_type_check
    CHECK (collection_type IN ('manual', 'synced', 'playlist', 'smart'));

-- 3. Move the existing abs_playlists row(s) into the canonical store.
--    is_public maps to is_shared; profile_id (uuid) is stringified.
INSERT INTO user_personal_collections
    (id, user_id, profile_id, name, description, collection_type,
     is_shared, created_at, updated_at, creator_profile_id)
SELECT
    id,
    user_id,
    COALESCE(profile_id::text, ''),
    name,
    description,
    'playlist',
    is_public,
    created_at,
    updated_at,
    COALESCE(profile_id::text, '')
FROM abs_playlists;

INSERT INTO user_personal_collection_items
    (user_id, collection_id, media_item_id, sub_item_id, position, added_at)
SELECT
    p.user_id,
    i.playlist_id,
    i.library_item_id,
    i.episode_id,
    i.position,
    i.added_at
FROM abs_playlist_items i
JOIN abs_playlists p ON p.id = i.playlist_id;

-- 4. Drop the abs_* collection tables. abs_playlist_items has a FK to
--    abs_playlists, so the order matters.
DROP TABLE abs_playlist_items;
DROP TABLE abs_playlists;
DROP TABLE abs_collection_items;
DROP TABLE abs_user_collections;
DROP TABLE abs_smart_collections;

-- 5. Media-type filter on page_sections. Default preserves current
--    behavior (existing rails surface movies+series only).
ALTER TABLE page_sections
    ADD COLUMN media_types text[] NOT NULL DEFAULT ARRAY['movie','series'];
```

- [ ] **Step 2: Verify the file compiles as valid SQL**

The repo embeds migrations via `migrations/embed.go`. The migration runner will syntax-check on load.

Run: `go build ./...`
Expected: clean (the embed package compiles fine even if a new file is added).

- [ ] **Step 3: Commit (up only — down comes next task)**

Don't commit yet. Commit happens after Task 2's down-migration so the up/down pair lands atomically.

---

## Task 2: Down-migration body

**Files:**
- Create: `migrations/156_unify_user_collections.down.sql`

The down migration is intentionally lossy in reverse: rolling back loses any `'playlist'`/`'smart'` collections that were created after the up. With the up bringing in 1 row, lossy reverse is acceptable.

- [ ] **Step 1: Find the abs_* CREATE TABLE statements to inline**

Read these files and copy their `CREATE TABLE` bodies (and `CREATE INDEX` statements; **omit** any `DROP TABLE IF EXISTS` boilerplate at the top — the down migration creates from clean state):

```bash
cat migrations/149_abs_user_collections.up.sql
cat migrations/150_abs_collection_items.up.sql
cat migrations/151_abs_playlists.up.sql
cat migrations/152_abs_playlist_items.up.sql
cat migrations/153_abs_smart_collections.up.sql
```

- [ ] **Step 2: Write the down migration**

```sql
-- Reverse migration 156. Lossy in reverse — playlist/smart rows
-- created after the up migration are deleted, not migrated back.

-- 1. Remove the page_sections column.
ALTER TABLE page_sections DROP COLUMN media_types;

-- 2. Recreate the abs_* tables empty. Schemas are inlined from the
--    original up migrations 149–153 — keep identical (column types,
--    constraint names, index names) so any tool keyed off those names
--    sees the same shape.
--
-- BEGIN inlined from migrations/149_abs_user_collections.up.sql
--   <PASTE EXACT CREATE TABLE + CREATE INDEX statements>
-- END inlined

-- BEGIN inlined from migrations/150_abs_collection_items.up.sql
--   <PASTE EXACT CREATE TABLE + CREATE INDEX statements>
-- END inlined

-- BEGIN inlined from migrations/151_abs_playlists.up.sql
--   <PASTE EXACT CREATE TABLE + CREATE INDEX + FK statements>
-- END inlined

-- BEGIN inlined from migrations/152_abs_playlist_items.up.sql
--   <PASTE EXACT CREATE TABLE + CREATE INDEX + FK statements>
-- END inlined

-- BEGIN inlined from migrations/153_abs_smart_collections.up.sql
--   <PASTE EXACT CREATE TABLE + CREATE INDEX statements>
-- END inlined

-- 3. Remove rows we promoted from abs_playlists during the up.
DELETE FROM user_personal_collection_items
    WHERE collection_id IN (
        SELECT id FROM user_personal_collections
        WHERE collection_type IN ('playlist', 'smart')
    );
DELETE FROM user_personal_collections
    WHERE collection_type IN ('playlist', 'smart');

-- 4. Restore the narrow CHECK constraint.
ALTER TABLE user_personal_collections
    DROP CONSTRAINT IF EXISTS user_personal_collections_type_check;
ALTER TABLE user_personal_collections
    ADD CONSTRAINT user_personal_collections_type_check
    CHECK (collection_type IN ('manual', 'synced'));

-- 5. Drop the sub_item_id column.
ALTER TABLE user_personal_collection_items DROP COLUMN sub_item_id;
```

Replace each `<PASTE EXACT ... statements>` block with the actual SQL from the corresponding `migrations/14X_abs_*.up.sql` file. Do NOT skip any constraint or index — the round-trip test below verifies parity.

- [ ] **Step 3: Verify SQL syntactic validity**

Run: `go build ./...`
Expected: clean.

- [ ] **Step 4: Commit the up+down pair**

```bash
git add migrations/156_unify_user_collections.up.sql migrations/156_unify_user_collections.down.sql
git commit -m "feat(migrations): 156 unify user collections

Adds sub_item_id to user_personal_collection_items, widens
collection_type to include 'playlist' and 'smart', adds media_types
filter to page_sections, moves the existing abs_playlists row into
the canonical store, and drops the abs_* collection tables.

Lays the storage foundation for sub-projects 2-4 to wire the
application against."
```

---

## Task 3: Migration round-trip test

**Files:**
- Create: `migrations/156_unify_user_collections_test.go` (only if a similar pattern exists in the repo)

The repo doesn't have a standard "migration round-trip test" harness today (verify with `find . -name "migration*_test.go" -o -name "*round_trip*_test.go" | head`). If no harness exists, this task is **best-effort manual verification**:

- [ ] **Step 1: Verify a harness pattern exists, or skip to manual verification**

Run:
```bash
find . -name "migration*_test.go" -not -path "*/node_modules/*" 2>/dev/null | head
grep -rln "migrate.Up\|migrate.Down\|migrate.Steps" --include="*_test.go" 2>/dev/null | head
```

If no harness emerges, fall through to Step 2 (manual). Otherwise add a Go test following the existing harness pattern that:
1. Migrates to 155.
2. Inserts a fake `abs_playlists` row (id `test-pl-1`, user 1, name `test`).
3. Inserts a fake `abs_playlist_items` row referencing it.
4. Migrates up to 156.
5. Asserts `user_personal_collections WHERE collection_type='playlist' AND id='test-pl-1'` exists and `user_personal_collection_items WHERE collection_id='test-pl-1'` exists.
6. Migrates down to 155.
7. Asserts the `abs_playlists` table exists and is empty (the row is GONE — down is lossy by design).

- [ ] **Step 2: Manual verification against a throwaway DB**

If no harness, do the following manually once and capture the output in the MR description:

```bash
# 1. Spin up an empty postgres
docker run --rm -d --name pgcheck -e POSTGRES_PASSWORD=x -p 5443:5432 pgvector/pgvector:pg18
sleep 5

# 2. Apply migrations up to 155 (use the existing migrate binary or schema dump)
#    Adapt this to whatever the repo uses to run migrations against an arbitrary DB.

# 3. Insert a fake abs_playlists + items row.
PGPASSWORD=x psql -h localhost -p 5443 -U postgres -d postgres -c "
    INSERT INTO abs_playlists(id, user_id, name) VALUES ('test-pl-1', 1, 'test');
    INSERT INTO abs_playlist_items(playlist_id, library_item_id, position) VALUES ('test-pl-1', 'foo', 0);
"

# 4. Apply migration 156.
# 5. Verify the row landed in user_personal_collections.
PGPASSWORD=x psql -h localhost -p 5443 -U postgres -d postgres -c "
    SELECT collection_type, name FROM user_personal_collections WHERE id='test-pl-1';
    SELECT collection_id, media_item_id, sub_item_id FROM user_personal_collection_items WHERE collection_id='test-pl-1';
"
# Expected: collection_type='playlist', name='test'; one item row with sub_item_id=''

# 6. Apply down migration.
# 7. Verify abs_playlists exists (empty) and user_personal_collections has no playlist rows.

# Cleanup
docker rm -f pgcheck
```

- [ ] **Step 3: If you added a Go test, commit it**

```bash
git add migrations/156_unify_user_collections_test.go
git commit -m "test(migrations): 156 round-trip verifies abs_playlists migrate to canonical"
```

If only manual verification was done, capture the output in the MR description instead — no commit needed.

---

## Verification (after merge)

1. **Dev DB** — run `make dev-backend` against a fresh DB. Migration 156 applies cleanly. `\d user_personal_collection_items` shows the `sub_item_id` column. `\d page_sections` shows `media_types`.

2. **Existing data migration** — on a copy of the production DB (or staging if available), run migration 156 and verify the single existing `abs_playlists` row is now in `user_personal_collections` with `collection_type='playlist'`:

   ```sql
   SELECT id, name, collection_type FROM user_personal_collections WHERE collection_type='playlist';
   ```

3. **abs_* tables gone** — confirm:

   ```sql
   SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_name LIKE 'abs_%collection%';
   -- Should return 0 rows.
   ```

   `abs_sessions`, `abs_bookmarks`, `abs_playback_sessions`, `abs_rss_feeds` remain — those aren't in scope here.

4. **Silo binary builds against the new schema** — `go build ./...` is green even though the ABS store adapters still reference the dropped tables. They reference them via SQL strings, so compile-time is fine; runtime will fail until sub-project 3 rewrites them. **Do NOT** deploy without sub-project 3, or stash this migration behind a feature flag.

---

## Self-Review

**Spec coverage:**
- `sub_item_id` column ✓ (Task 1)
- `collection_type` widened to playlist/smart ✓ (Task 1)
- Move existing abs_playlists row ✓ (Task 1)
- Drop abs_* tables ✓ (Task 1)
- `page_sections.media_types` ✓ (Task 1)
- Down migration ✓ (Task 2)
- Round-trip test ✓ (Task 3, best-effort)

**Placeholder scan:** The `<PASTE EXACT ... statements>` blocks in Task 2 Step 2 are template placeholders that the implementer must fill in by copying from the existing migration files. Each is bounded by explicit BEGIN/END comment markers naming the source file. The instructions in Step 1 say to read those files first; Step 2's block then becomes literal SQL. Not a "TBD" in the bad sense — it's a deliberate copy-from-source step.

**Type consistency:** `sub_item_id` (text, NOT NULL DEFAULT '') consistent in up + down + verification queries. `collection_type` enum values consistent across the spec, up, down, and CHECK constraint.

**Risk:** All schema-level, no application changes. The biggest risk is forgetting to ship sub-project 3 in the same release — runtime will start erroring on ABS endpoints the moment migration 156 applies, because the adapters still query the dropped tables.
