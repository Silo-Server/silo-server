-- Set-based replacement for silo_rename_content_id (20260614120000).
--
-- Re-anchoring a provider-anchored series moves the series and every season
-- and episode whose content_id embeds the old anchor. silo_rename_content_id
-- moves one value per call and walks every soft-reference column each time,
-- so a long-running series would cost one catalog walk per episode.
-- silo_rename_content_ids(from[], to[]) moves all pairs with one UPDATE per
-- column, and carries the availability merge from 20260625172243. FK children
-- follow via ON UPDATE CASCADE (20260614120000, 20260925011258).
-- silo_rename_content_id becomes a wrapper around it, so there is one rename
-- body to maintain.
--
-- The caller guarantees that every target id is free in the catalog
-- (media_items, seasons, episodes) and that no id appears as both a source and
-- a target. Soft references can still hold a target id: per-user rows outlive
-- a deleted item. Where one collides on a unique key with a moving row, watch
-- progress keeps the newer row and every other table keeps the row already on
-- the target id, the policies internal/catalog/reattribute applies to a merge.

-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION silo_rename_content_ids(p_from text[], p_to text[])
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    r RECORD;
    i integer;
    rels regclass[];
    cols name[];
BEGIN
    IF p_from IS NULL OR p_to IS NULL OR cardinality(p_from) = 0 THEN
        RETURN;
    END IF;
    IF cardinality(p_from) <> cardinality(p_to) THEN
        RAISE EXCEPTION 'silo_rename_content_ids: % sources but % targets',
            cardinality(p_from), cardinality(p_to);
    END IF;

    -- Availability rows are insert-only history, so rows for a target id can
    -- outlive a deleted item. Merge them into the source rows (keeping the
    -- earliest timestamps) before the scalar rewrite below hits the unique
    -- availability keys: the logical key when the series moves, and the
    -- primary key when an episode id moves.
    IF to_regclass('public.episode_availability') IS NOT NULL THEN
        WITH pairs AS (
            SELECT from_id, to_id FROM unnest(p_from, p_to) AS m(from_id, to_id)
        ),
        conflicts AS (
            SELECT src.library_id,
                   src.episode_id AS source_episode_id,
                   dest.episode_id AS target_episode_id,
                   dest.available_at,
                   dest.created_at
            FROM pairs
            JOIN public.episode_availability src ON src.series_id = pairs.from_id
            JOIN public.episode_availability dest
              ON dest.library_id = src.library_id
             AND dest.series_id = pairs.to_id
             AND dest.episode_key = src.episode_key
            UNION
            SELECT src.library_id,
                   src.episode_id,
                   dest.episode_id,
                   dest.available_at,
                   dest.created_at
            FROM pairs
            JOIN public.episode_availability src ON src.episode_id = pairs.from_id
            JOIN public.episode_availability dest
              ON dest.library_id = src.library_id
             AND dest.episode_id = pairs.to_id
        ),
        merged AS (
            SELECT library_id,
                   source_episode_id,
                   MIN(available_at) AS available_at,
                   MIN(created_at) AS created_at
            FROM conflicts
            GROUP BY library_id, source_episode_id
        ),
        updated_source AS (
            UPDATE public.episode_availability src
            SET available_at = LEAST(src.available_at, merged.available_at),
                created_at = LEAST(src.created_at, merged.created_at)
            FROM merged
            WHERE src.library_id = merged.library_id
              AND src.episode_id = merged.source_episode_id
            RETURNING src.library_id, src.episode_id
        )
        DELETE FROM public.episode_availability dest
        USING conflicts
        WHERE dest.library_id = conflicts.library_id
          AND dest.episode_id = conflicts.target_episode_id
          AND EXISTS (
              SELECT 1
              FROM updated_source u
              WHERE u.library_id = conflicts.library_id
                AND u.episode_id = conflicts.source_episode_id
          );
    END IF;

    IF to_regclass('public.movie_availability') IS NOT NULL THEN
        WITH pairs AS (
            SELECT from_id, to_id FROM unnest(p_from, p_to) AS m(from_id, to_id)
        ),
        conflicts AS (
            SELECT
                src.library_id,
                src.item_id AS source_item_id,
                dest.item_id AS target_item_id,
                LEAST(src.available_at, dest.available_at) AS available_at,
                LEAST(src.created_at, dest.created_at) AS created_at
            FROM pairs
            JOIN public.movie_availability src ON src.item_id = pairs.from_id
            JOIN public.movie_availability dest
              ON dest.library_id = src.library_id
             AND dest.item_id = pairs.to_id
        ),
        updated_source AS (
            UPDATE public.movie_availability src
            SET available_at = conflicts.available_at,
                created_at = conflicts.created_at
            FROM conflicts
            WHERE src.library_id = conflicts.library_id
              AND src.item_id = conflicts.source_item_id
            RETURNING src.library_id, src.item_id
        )
        DELETE FROM public.movie_availability dest
        USING conflicts
        WHERE dest.library_id = conflicts.library_id
          AND dest.item_id = conflicts.target_item_id
          AND EXISTS (
              SELECT 1
              FROM updated_source u
              WHERE u.library_id = conflicts.library_id
                AND u.item_id = conflicts.source_item_id
          );
    END IF;

    -- A profile can hold progress on both ids. Keep the newer row, as
    -- reattribute.moveProgressPairs does: drop an older target row here, and
    -- the collision handling below drops a source row that is not newer.
    IF to_regclass('public.user_watch_progress') IS NOT NULL THEN
        DELETE FROM public.user_watch_progress dest
        USING unnest(p_from, p_to) AS m(from_id, to_id), public.user_watch_progress src
        WHERE dest.media_item_id = m.to_id
          AND src.media_item_id = m.from_id
          AND src.user_id = dest.user_id
          AND src.profile_id = dest.profile_id
          AND src.updated_at > dest.updated_at;
    END IF;

    SELECT array_agg(refs.rel), array_agg(refs.col)
    INTO rels, cols
    FROM (
        SELECT cl.oid::regclass AS rel, a.attname AS col
        FROM pg_class cl
        JOIN pg_namespace n ON n.oid = cl.relnamespace
        JOIN pg_attribute a ON a.attrelid = cl.oid AND a.attnum > 0 AND NOT a.attisdropped
        JOIN pg_type t ON t.oid = a.atttypid
        WHERE cl.relkind IN ('r', 'p')
          AND n.nspname = 'public'
          AND t.typname IN ('text', 'varchar', 'bpchar')
          AND a.attname IN (
                'media_item_id', 'series_id', 'season_id', 'episode_id', 'content_id',
                'season_content_id', 'episode_content_id', 'library_item_id', 'cover_item',
                'item_id', 'similar_item_id', 'source_item_id'
              )
          AND cl.relname NOT LIKE 'content_id_migration%'
          -- Skip real FK children of the family; ON UPDATE CASCADE moves those.
          AND NOT EXISTS (
                SELECT 1
                FROM pg_constraint con
                JOIN unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord) ON TRUE
                WHERE con.contype = 'f'
                  AND con.conrelid = cl.oid
                  AND con.confrelid IN ('media_items'::regclass, 'seasons'::regclass, 'episodes'::regclass)
                  AND k.attnum = a.attnum
              )
    ) AS refs;

    -- Move every column set-based. A unique collision is rare, so it is
    -- handled on a second pass rather than paying a subtransaction per column
    -- on every rename.
    BEGIN
        FOR i IN 1 .. coalesce(cardinality(rels), 0) LOOP
            EXECUTE format(
                'UPDATE %s AS t SET %I = m.to_id
                   FROM unnest($1::text[], $2::text[]) AS m(from_id, to_id)
                  WHERE t.%I = m.from_id',
                rels[i], cols[i], cols[i])
                USING p_from, p_to;
        END LOOP;
    EXCEPTION WHEN unique_violation THEN
        -- The first pass rolled back. Move each column again; when one hits a
        -- unique key, move its rows one at a time and drop a row whose target
        -- duplicate already exists. A catalog row is never dropped: a taken
        -- catalog id breaks the caller's guarantee, so the rename fails.
        FOR i IN 1 .. coalesce(cardinality(rels), 0) LOOP
            BEGIN
                EXECUTE format(
                    'UPDATE %s AS t SET %I = m.to_id
                       FROM unnest($1::text[], $2::text[]) AS m(from_id, to_id)
                      WHERE t.%I = m.from_id',
                    rels[i], cols[i], cols[i])
                    USING p_from, p_to;
            EXCEPTION WHEN unique_violation THEN
                IF rels[i] IN ('media_items'::regclass, 'seasons'::regclass, 'episodes'::regclass) THEN
                    RAISE;
                END IF;
                FOR r IN EXECUTE format(
                    'SELECT t.tableoid AS row_rel, t.ctid AS row_ctid, m.to_id
                       FROM %s AS t
                       JOIN unnest($1::text[], $2::text[]) AS m(from_id, to_id)
                         ON t.%I = m.from_id',
                    rels[i], cols[i])
                    USING p_from, p_to
                LOOP
                    BEGIN
                        EXECUTE format('UPDATE %s SET %I = $1 WHERE tableoid = $2 AND ctid = $3', rels[i], cols[i])
                            USING r.to_id, r.row_rel, r.row_ctid;
                    EXCEPTION WHEN unique_violation THEN
                        EXECUTE format('DELETE FROM %s WHERE tableoid = $1 AND ctid = $2', rels[i])
                            USING r.row_rel, r.row_ctid;
                    END;
                END LOOP;
            END;
        END LOOP;
    END;

    IF to_regclass('public.trending_discover_snapshots') IS NOT NULL THEN
        FOR i IN 1 .. cardinality(p_from) LOOP
            UPDATE trending_discover_snapshots
            SET content_ids = array_replace(content_ids, p_from[i], p_to[i])
            WHERE p_from[i] = ANY(content_ids);
        END LOOP;
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION silo_rename_content_id(p_from text, p_to text)
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    IF p_from IS NULL OR p_to IS NULL OR p_from = p_to THEN
        RETURN;
    END IF;
    PERFORM silo_rename_content_ids(ARRAY[p_from], ARRAY[p_to]);
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- Restore silo_rename_content_id's own body from
-- 20260625172243_dedupe_episode_availability_during_reid.sql before dropping
-- the function it now wraps.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION silo_rename_content_id(p_from text, p_to text)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    c RECORD;
BEGIN
    IF p_from IS NULL OR p_to IS NULL OR p_from = p_to THEN
        RETURN;
    END IF;

    IF to_regclass('public.episode_availability') IS NOT NULL THEN
        WITH conflicts AS (
            SELECT
                src.library_id,
                src.episode_id AS source_episode_id,
                dest.episode_id AS target_episode_id,
                LEAST(src.available_at, dest.available_at) AS available_at,
                LEAST(src.created_at, dest.created_at) AS created_at
            FROM public.episode_availability src
            JOIN public.episode_availability dest
              ON dest.library_id = src.library_id
             AND dest.series_id = p_to
             AND dest.episode_key = src.episode_key
            WHERE src.series_id = p_from
        ),
        updated_source AS (
            UPDATE public.episode_availability src
            SET available_at = conflicts.available_at,
                created_at = conflicts.created_at
            FROM conflicts
            WHERE src.library_id = conflicts.library_id
              AND src.episode_id = conflicts.source_episode_id
            RETURNING src.library_id, src.episode_id
        )
        DELETE FROM public.episode_availability dest
        USING conflicts
        WHERE dest.library_id = conflicts.library_id
          AND dest.episode_id = conflicts.target_episode_id
          AND EXISTS (
              SELECT 1
              FROM updated_source u
              WHERE u.library_id = conflicts.library_id
                AND u.episode_id = conflicts.source_episode_id
          );
    END IF;

    IF to_regclass('public.movie_availability') IS NOT NULL THEN
        WITH conflicts AS (
            SELECT
                src.library_id,
                LEAST(src.available_at, dest.available_at) AS available_at,
                LEAST(src.created_at, dest.created_at) AS created_at
            FROM public.movie_availability src
            JOIN public.movie_availability dest
              ON dest.library_id = src.library_id
             AND dest.item_id = p_to
            WHERE src.item_id = p_from
        ),
        updated_source AS (
            UPDATE public.movie_availability src
            SET available_at = conflicts.available_at,
                created_at = conflicts.created_at
            FROM conflicts
            WHERE src.library_id = conflicts.library_id
              AND src.item_id = p_from
            RETURNING src.library_id, src.item_id
        )
        DELETE FROM public.movie_availability dest
        USING conflicts
        WHERE dest.library_id = conflicts.library_id
          AND dest.item_id = p_to
          AND EXISTS (
              SELECT 1
              FROM updated_source u
              WHERE u.library_id = conflicts.library_id
                AND u.item_id = p_from
          );
    END IF;

    FOR c IN
        SELECT cl.oid::regclass AS rel, a.attname AS col
        FROM pg_class cl
        JOIN pg_namespace n ON n.oid = cl.relnamespace
        JOIN pg_attribute a ON a.attrelid = cl.oid AND a.attnum > 0 AND NOT a.attisdropped
        JOIN pg_type t ON t.oid = a.atttypid
        WHERE cl.relkind IN ('r', 'p')
          AND n.nspname = 'public'
          AND t.typname IN ('text', 'varchar', 'bpchar')
          AND a.attname IN (
                'media_item_id', 'series_id', 'season_id', 'episode_id', 'content_id',
                'season_content_id', 'episode_content_id', 'library_item_id', 'cover_item',
                'item_id', 'similar_item_id', 'source_item_id'
              )
          AND cl.relname NOT LIKE 'content_id_migration%'
          -- Skip real FK children of the family; ON UPDATE CASCADE moves those.
          AND NOT EXISTS (
                SELECT 1
                FROM pg_constraint con
                JOIN unnest(con.conkey) WITH ORDINALITY AS k(attnum, ord) ON TRUE
                WHERE con.contype = 'f'
                  AND con.conrelid = cl.oid
                  AND con.confrelid IN ('media_items'::regclass, 'seasons'::regclass, 'episodes'::regclass)
                  AND k.attnum = a.attnum
              )
    LOOP
        EXECUTE format('UPDATE %s SET %I = $2 WHERE %I = $1', c.rel, c.col, c.col)
            USING p_from, p_to;
    END LOOP;

    -- Array-valued soft references are not covered by the scalar loop above
    -- (text[] is not in the type filter and cannot carry an FK), matching the
    -- gap closed in 20260612130000 Step 6b. Negligible at runtime because a
    -- freshly matched local item is rarely already in a trending snapshot, but
    -- kept in lockstep so a rename never leaves a stale array element.
    IF to_regclass('public.trending_discover_snapshots') IS NOT NULL THEN
        UPDATE trending_discover_snapshots
        SET content_ids = array_replace(content_ids, p_from, p_to)
        WHERE p_from = ANY(content_ids);
    END IF;
END;
$$;
-- +goose StatementEnd

DROP FUNCTION IF EXISTS silo_rename_content_ids(text[], text[]);
