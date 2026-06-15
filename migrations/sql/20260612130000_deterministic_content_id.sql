-- Deterministic, cross-server content_id (see
-- docs/architecture/deterministic-content-id.md).
--
-- This migration remaps the *values* of content_id (and every column that
-- references it) from per-server Sonyflake ids to provider-derived structured
-- keys, and changes the storage collation of those columns to "C". The column
-- *type* does not change (text -> text COLLATE "C"), so the soft-reference graph
-- keeps working; only the values and collation change.
--
-- Mapping rules (kept in lockstep with internal/contentid; SchemeVersion = 1):
--   movies   anchor precedence tmdb -> imdb -> tvdb  => movie-<provider>-<id>
--   series   anchor precedence tvdb -> tmdb -> imdb  => series-<provider>-<id>
--   seasons  compose from the series anchor          => season-<provider>-<sid>-<n>
--   episodes compose from the series anchor          => episode-<provider>-<sid>-<s>-<e>
-- The "-" component separator is an RFC 3986 unreserved char, so the id needs no
-- URL encoding (see internal/contentid). Items with no usable provider anchor
-- (and all item types other than movie/series, e.g. audiobook/ebook/podcast)
-- keep their existing Sonyflake id: there is no stable cross-server anchor to
-- derive from, so changing them gains nothing. New unmatched movies/series
-- instead get a path-derived local- id at
-- scan time (handled in Go, not here).
--
-- Safety: collisions (two ids deriving to the same key, or a key already taken
-- by an un-mapped row) are detected and left on Sonyflake — the migration never
-- merges or drops a row. The old->new map is retained in
-- content_id_migration_map for audit and rollback.
--
-- OPERATIONAL NOTE: on a large production dataset (10-50M rows) the value
-- UPDATEs and the COLLATE rewrite hold AccessExclusive locks for the duration.
-- Run off-peak. For very large installs, replace the in-transaction UPDATE loop
-- below with the batched/online procedure in the design doc (§7.2); the mapping
-- and collision logic here are reusable as-is.

-- +goose Up

-- Step 1: persistent audit/rollback map.
CREATE TABLE content_id_migration_map (
    old_id text PRIMARY KEY,
    new_id text NOT NULL,
    entity text NOT NULL,                     -- media_item | season | episode
    status text NOT NULL DEFAULT 'mapped'     -- mapped | collision
);

-- Step 2a: derive movie/series keys from the denormalized provider columns,
-- applying the frozen precedence. Rows that already hold a non-Sonyflake key
-- (new_id = old_id) or have no usable anchor are skipped.
INSERT INTO content_id_migration_map (old_id, new_id, entity)
SELECT old_id, new_id, 'media_item'
FROM (
    SELECT
        mi.content_id AS old_id,
        CASE
            WHEN lower(mi.type) IN ('movie', 'movies') THEN
                CASE
                    WHEN nullif(btrim(mi.tmdb_id, E' \t\n\r\f'), '') ~ '^[0-9]+$'      THEN 'movie-tmdb-' || btrim(mi.tmdb_id, E' \t\n\r\f')
                    WHEN lower(btrim(mi.imdb_id, E' \t\n\r\f')) ~ '^tt[0-9]+$'         THEN 'movie-imdb-' || lower(btrim(mi.imdb_id, E' \t\n\r\f'))
                    WHEN nullif(btrim(mi.tvdb_id, E' \t\n\r\f'), '') ~ '^[0-9]+$'      THEN 'movie-tvdb-' || btrim(mi.tvdb_id, E' \t\n\r\f')
                END
            WHEN lower(mi.type) IN ('series', 'show', 'tv') THEN
                CASE
                    WHEN nullif(btrim(mi.tvdb_id, E' \t\n\r\f'), '') ~ '^[0-9]+$'      THEN 'series-tvdb-' || btrim(mi.tvdb_id, E' \t\n\r\f')
                    WHEN nullif(btrim(mi.tmdb_id, E' \t\n\r\f'), '') ~ '^[0-9]+$'      THEN 'series-tmdb-' || btrim(mi.tmdb_id, E' \t\n\r\f')
                    WHEN lower(btrim(mi.imdb_id, E' \t\n\r\f')) ~ '^tt[0-9]+$'         THEN 'series-imdb-' || lower(btrim(mi.imdb_id, E' \t\n\r\f'))
                END
        END AS new_id
    FROM media_items mi
) d
WHERE d.new_id IS NOT NULL
  AND d.new_id <> d.old_id;

-- Step 2b: seasons compose from their series' new key. Joining on the map means
-- only seasons of an already-mapped (legacy) series are remapped; seasons of a
-- post-cutover series (series_id already structured) naturally fall out.
INSERT INTO content_id_migration_map (old_id, new_id, entity)
SELECT
    s.content_id,
    'season-' || split_part(m.new_id, '-', 2) || '-' || split_part(m.new_id, '-', 3)
        || '-' || s.season_number,
    'season'
FROM seasons s
JOIN content_id_migration_map m
    ON m.old_id = s.series_id AND m.entity = 'media_item'
WHERE m.new_id LIKE 'series-%';

-- Step 2c: episodes compose from the series anchor + season/episode numbers.
INSERT INTO content_id_migration_map (old_id, new_id, entity)
SELECT
    e.content_id,
    'episode-' || split_part(m.new_id, '-', 2) || '-' || split_part(m.new_id, '-', 3)
        || '-' || e.season_number || '-' || e.episode_number,
    'episode'
FROM episodes e
JOIN content_id_migration_map m
    ON m.old_id = e.series_id AND m.entity = 'media_item'
WHERE m.new_id LIKE 'series-%';

-- Step 3: collision detection. Never remap into a key that is claimed twice, or
-- one already occupied by a row that is NOT being remapped (a pre-existing
-- deterministic row). Such rows stay on Sonyflake; operators reconcile dupes
-- separately. This guarantees the value remap can never violate a PK.
CREATE INDEX content_id_migration_map_new_id_idx ON content_id_migration_map (new_id);

UPDATE content_id_migration_map m
SET status = 'collision'
WHERE m.new_id IN (
    SELECT new_id FROM content_id_migration_map GROUP BY new_id HAVING count(*) > 1
);

UPDATE content_id_migration_map m
SET status = 'collision'
WHERE m.status = 'mapped'
  AND (
        EXISTS (SELECT 1 FROM media_items x WHERE x.content_id = m.new_id)
     OR EXISTS (SELECT 1 FROM seasons     x WHERE x.content_id = m.new_id)
     OR EXISTS (SELECT 1 FROM episodes    x WHERE x.content_id = m.new_id)
  );

-- Cascade collisions from a series to its children. A season/episode key embeds
-- the series anchor (season-<p>-<sid>-..., episode-<p>-<sid>-...), so if the
-- series itself was left on Sonyflake (collision), remapping a child would point
-- the embedded anchor at a series row that no longer exists under that key —
-- orphaning the child and dropping its rows from the anchor-derived history
-- query. Keep such children on Sonyflake too. (A collision on the season alone
-- does not force the episode, whose anchor is the series, not the season.)
UPDATE content_id_migration_map m
SET status = 'collision'
FROM seasons s
JOIN content_id_migration_map ms ON ms.old_id = s.series_id AND ms.status = 'collision'
WHERE m.entity = 'season' AND m.status = 'mapped' AND m.old_id = s.content_id;

UPDATE content_id_migration_map m
SET status = 'collision'
FROM episodes e
JOIN content_id_migration_map ms ON ms.old_id = e.series_id AND ms.status = 'collision'
WHERE m.entity = 'episode' AND m.status = 'mapped' AND m.old_id = e.content_id;

-- Step 4: enumerate every column in the content_id reference graph — the three
-- PKs, every FK child column referencing them, and a name sweep for the
-- unconstrained soft references — so the remap and collation change cover them
-- all without trusting a hand-list (per the design doc).
CREATE TEMP TABLE _cid_cols (table_name regclass, column_name name) ON COMMIT DROP;

-- The three primary keys.
INSERT INTO _cid_cols VALUES
    ('media_items'::regclass, 'content_id'),
    ('seasons'::regclass,     'content_id'),
    ('episodes'::regclass,    'content_id');

-- Every FK child column that references the family (reliable, from the catalog).
INSERT INTO _cid_cols
SELECT con.conrelid::regclass, att.attname
FROM pg_constraint con
JOIN unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord) ON TRUE
JOIN pg_attribute att ON att.attrelid = con.conrelid AND att.attnum = k.attnum
WHERE con.contype = 'f'
  AND con.confrelid IN ('media_items'::regclass, 'seasons'::regclass, 'episodes'::regclass);

-- Soft references (no FK): text columns whose name is a known content-id holder
-- in this schema. The value remap is self-protecting — only values that match a
-- mapped Sonyflake id change — so an over-broad name match cannot corrupt
-- unrelated data; it only needs the names to genuinely hold content ids.
INSERT INTO _cid_cols
SELECT c.oid::regclass, a.attname
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
JOIN pg_type t ON t.oid = a.atttypid
WHERE c.relkind IN ('r', 'p')
  AND n.nspname = 'public'
  AND t.typname IN ('text', 'varchar', 'bpchar')
  AND a.attname IN (
        'media_item_id', 'series_id', 'season_id', 'episode_id', 'content_id',
        'season_content_id', 'episode_content_id', 'library_item_id', 'cover_item',
        'item_id', 'similar_item_id', 'source_item_id'
      )
  AND c.relname NOT LIKE 'content_id_migration%'
  AND NOT EXISTS (
        SELECT 1 FROM _cid_cols ex WHERE ex.table_name = c.oid::regclass AND ex.column_name = a.attname
      );

-- Step 5: drop the family's FK constraints (so PK and child values can be
-- remapped and recollated), saving their definitions to recreate afterward.
CREATE TEMP TABLE content_id_migration_fk (conname name, rel regclass, condef text) ON COMMIT DROP;

-- +goose StatementBegin
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN
        SELECT con.conname, con.conrelid::regclass AS rel, pg_get_constraintdef(con.oid) AS condef
        FROM pg_constraint con
        WHERE con.contype = 'f'
          AND con.confrelid IN ('media_items'::regclass, 'seasons'::regclass, 'episodes'::regclass)
    LOOP
        INSERT INTO content_id_migration_fk (conname, rel, condef) VALUES (r.conname, r.rel, r.condef);
        EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', r.rel, r.conname);
    END LOOP;
END $$;
-- +goose StatementEnd

-- Step 5b: drop every user trigger on a swept table for the duration. Two
-- reasons: (1) a trigger that names a family column in an UPDATE OF list or WHEN
-- clause blocks ALTER COLUMN TYPE; (2) ANY row trigger on a swept table would
-- fire per-row during the remap (e.g. the episode_catalog_entries
-- denormalization triggers — including plain AFTER INSERT/UPDATE/DELETE ones a
-- column-name match would miss), causing per-row plpgsql work and order-
-- dependent corruption of the denormalized tables. Those tables are remapped
-- directly by the column sweep, so dropping all of their triggers is both
-- correct and faster. Recreated verbatim in Step 8.
CREATE TEMP TABLE content_id_migration_trg (tgname name, rel regclass, def text) ON COMMIT DROP;

-- +goose StatementBegin
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN
        SELECT t.tgname, t.tgrelid::regclass AS rel, pg_get_triggerdef(t.oid) AS def
        FROM pg_trigger t
        WHERE NOT t.tgisinternal
          AND t.tgrelid IN (SELECT table_name FROM _cid_cols)
    LOOP
        INSERT INTO content_id_migration_trg (tgname, rel, def) VALUES (r.tgname, r.rel, r.def);
        EXECUTE format('DROP TRIGGER %I ON %s', r.tgname, r.rel);
    END LOOP;
END $$;
-- +goose StatementEnd

-- Step 6: remap values across every family column. After a row's value becomes
-- the (non-Sonyflake) new id it no longer matches any old_id, so a single pass
-- per column suffices and is idempotent.
-- +goose StatementBegin
DO $$
DECLARE
    c RECORD;
BEGIN
    FOR c IN SELECT table_name, column_name FROM _cid_cols LOOP
        EXECUTE format(
            'UPDATE %s t SET %I = m.new_id FROM content_id_migration_map m '
            || 'WHERE m.status = ''mapped'' AND m.old_id = t.%I',
            c.table_name, c.column_name, c.column_name
        );
    END LOOP;
END $$;
-- +goose StatementEnd

-- Step 6b: remap array-valued soft references. A text[] column that holds
-- resolved content_ids is excluded from the scalar sweep above on BOTH axes —
-- the type filter is scalar (text/varchar/bpchar) and the name list has
-- 'content_id', not the plural 'content_ids' — and an array cannot carry an FK,
-- so nothing else would touch it. Without this it keeps stale Sonyflake ids that
-- resolve to nothing. trending_discover_snapshots.content_ids is the only such
-- column in this schema; remap element-wise, preserving order, leaving unmapped
-- elements (collisions / unmatched) untouched. The WHERE EXISTS guard skips
-- empty and unaffected arrays so array_agg can never collapse the NOT NULL
-- column to NULL.
-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('public.trending_discover_snapshots') IS NOT NULL THEN
        UPDATE trending_discover_snapshots t
        SET content_ids = (
            SELECT array_agg(COALESCE(m.new_id, u.elem) ORDER BY u.ord)
            FROM unnest(t.content_ids) WITH ORDINALITY AS u(elem, ord)
            LEFT JOIN content_id_migration_map m
                ON m.status = 'mapped' AND m.old_id = u.elem
        )
        WHERE EXISTS (
            SELECT 1
            FROM unnest(t.content_ids) AS e(elem)
            JOIN content_id_migration_map m ON m.status = 'mapped' AND m.old_id = e.elem
        );
    END IF;
END $$;
-- +goose StatementEnd

-- Step 7: change collation of every family column to "C". This rewrites the
-- columns' indexes; structured keys share long prefixes, so "C" (memcmp) is
-- load-bearing — without it ordered/probe paths regress below the Sonyflake
-- status quo (design doc §5.2).
-- +goose StatementBegin
DO $$
DECLARE
    c RECORD;
BEGIN
    FOR c IN SELECT table_name, column_name FROM _cid_cols LOOP
        EXECUTE format(
            'ALTER TABLE %s ALTER COLUMN %I TYPE text COLLATE "C"',
            c.table_name, c.column_name
        );
    END LOOP;
END $$;
-- +goose StatementEnd

-- Step 8: recreate triggers, then the FK constraints (both sides now remapped
-- and "C"-collated).
-- +goose StatementBegin
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN SELECT def FROM content_id_migration_trg LOOP
        EXECUTE r.def;
    END LOOP;
    FOR r IN SELECT conname, rel, condef FROM content_id_migration_fk LOOP
        EXECUTE format('ALTER TABLE %s ADD CONSTRAINT %I %s', r.rel, r.conname, r.condef);
    END LOOP;
END $$;
-- +goose StatementEnd

-- NOTE: the design doc (§9.2.2) also proposes an expression index on
-- user_watch_history backing a "by show" display_id. It is intentionally NOT
-- created here: the current hot query (full-history DISTINCT ON) cannot use it
-- (its display_id references the joined episodes table and it lacks watched_at),
-- and it would index the wrong value for legacy/local episode rows (§9.2.5).
-- It belongs with the O(page) summary-table work (§9.2.3) that actually reads
-- it, using a resolved display_id that handles those rows correctly. Adding it
-- now would be pure write-amplification on every watch event with no reader.

-- +goose Down

-- Rebuild the column set for the reverse remap and collation reset.
CREATE TEMP TABLE _cid_cols (table_name regclass, column_name name) ON COMMIT DROP;
INSERT INTO _cid_cols VALUES
    ('media_items'::regclass, 'content_id'),
    ('seasons'::regclass,     'content_id'),
    ('episodes'::regclass,    'content_id');
INSERT INTO _cid_cols
SELECT con.conrelid::regclass, att.attname
FROM pg_constraint con
JOIN unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord) ON TRUE
JOIN pg_attribute att ON att.attrelid = con.conrelid AND att.attnum = k.attnum
WHERE con.contype = 'f'
  AND con.confrelid IN ('media_items'::regclass, 'seasons'::regclass, 'episodes'::regclass);
INSERT INTO _cid_cols
SELECT c.oid::regclass, a.attname
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
JOIN pg_type t ON t.oid = a.atttypid
WHERE c.relkind IN ('r', 'p')
  AND n.nspname = 'public'
  AND t.typname IN ('text', 'varchar', 'bpchar')
  AND a.attname IN (
        'media_item_id', 'series_id', 'season_id', 'episode_id', 'content_id',
        'season_content_id', 'episode_content_id', 'library_item_id', 'cover_item',
        'item_id', 'similar_item_id', 'source_item_id'
      )
  AND c.relname NOT LIKE 'content_id_migration%'
  AND NOT EXISTS (
        SELECT 1 FROM _cid_cols ex WHERE ex.table_name = c.oid::regclass AND ex.column_name = a.attname
      );

-- Drop FKs and family triggers, revert values via the map, reset collation to
-- the database default, recreate triggers + FKs. NOTE: rollback is only
-- consistent for rows minted before the generation cutover; rows created with
-- structured ids after cutover have no map entry and remain structured
-- (design doc §7.2).
CREATE TEMP TABLE content_id_migration_fk (conname name, rel regclass, condef text) ON COMMIT DROP;
CREATE TEMP TABLE content_id_migration_trg (tgname name, rel regclass, def text) ON COMMIT DROP;

-- +goose StatementBegin
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN
        SELECT con.conname, con.conrelid::regclass AS rel, pg_get_constraintdef(con.oid) AS condef
        FROM pg_constraint con
        WHERE con.contype = 'f'
          AND con.confrelid IN ('media_items'::regclass, 'seasons'::regclass, 'episodes'::regclass)
    LOOP
        INSERT INTO content_id_migration_fk (conname, rel, condef) VALUES (r.conname, r.rel, r.condef);
        EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', r.rel, r.conname);
    END LOOP;
    FOR r IN
        SELECT t.tgname, t.tgrelid::regclass AS rel, pg_get_triggerdef(t.oid) AS def
        FROM pg_trigger t
        WHERE NOT t.tgisinternal
          AND t.tgrelid IN (SELECT table_name FROM _cid_cols)
    LOOP
        INSERT INTO content_id_migration_trg (tgname, rel, def) VALUES (r.tgname, r.rel, r.def);
        EXECUTE format('DROP TRIGGER %I ON %s', r.tgname, r.rel);
    END LOOP;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE
    c RECORD;
BEGIN
    FOR c IN SELECT table_name, column_name FROM _cid_cols LOOP
        EXECUTE format(
            'UPDATE %s t SET %I = m.old_id FROM content_id_migration_map m '
            || 'WHERE m.status = ''mapped'' AND m.new_id = t.%I',
            c.table_name, c.column_name, c.column_name
        );
        EXECUTE format(
            'ALTER TABLE %s ALTER COLUMN %I TYPE text COLLATE pg_catalog."default"',
            c.table_name, c.column_name
        );
    END LOOP;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN SELECT def FROM content_id_migration_trg LOOP
        EXECUTE r.def;
    END LOOP;
    FOR r IN SELECT conname, rel, condef FROM content_id_migration_fk LOOP
        EXECUTE format('ALTER TABLE %s ADD CONSTRAINT %I %s', r.rel, r.conname, r.condef);
    END LOOP;
END $$;
-- +goose StatementEnd

-- Reverse the array-valued soft-reference remap (mirror of Step 6b). Runs while
-- content_id_migration_map still exists, i.e. before the DROP TABLE below.
-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('public.trending_discover_snapshots') IS NOT NULL THEN
        UPDATE trending_discover_snapshots t
        SET content_ids = (
            SELECT array_agg(COALESCE(m.old_id, u.elem) ORDER BY u.ord)
            FROM unnest(t.content_ids) WITH ORDINALITY AS u(elem, ord)
            LEFT JOIN content_id_migration_map m
                ON m.status = 'mapped' AND m.new_id = u.elem
        )
        WHERE EXISTS (
            SELECT 1
            FROM unnest(t.content_ids) AS e(elem)
            JOIN content_id_migration_map m ON m.status = 'mapped' AND m.new_id = e.elem
        );
    END IF;
END $$;
-- +goose StatementEnd

DROP TABLE IF EXISTS content_id_migration_map;
