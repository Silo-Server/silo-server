-- +goose Up
-- `silo_rename_content_id` updates soft-reference columns after a local item
-- receives its provider-backed deterministic ID. Availability tables are
-- insert-only historical facts, so stale rows can already exist for the target
-- ID even when the target media_items row does not. Merge those rows before the
-- broad scalar rewrite so unique availability keys do not block manual
-- matching.
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

-- +goose Down
-- Restore the pre-dedupe rename function from
-- 20260614120000_content_id_online_reid.sql.
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

    IF to_regclass('public.trending_discover_snapshots') IS NOT NULL THEN
        UPDATE trending_discover_snapshots
        SET content_ids = array_replace(content_ids, p_from, p_to)
        WHERE p_from = ANY(content_ids);
    END IF;
END;
$$;
-- +goose StatementEnd
