-- Online re-ID support for content_id (see
-- docs/architecture/deterministic-content-id.md, "Re-ID on match").
--
-- The deterministic-content-id migration (20260612130000) does a one-shot,
-- whole-table value remap under AccessExclusive locks. This migration instead
-- makes a *single* content_id value cheap to move at runtime, so an untagged
-- item that only learns its provider IDs at match time can be promoted from its
-- local: placeholder to its deterministic id in place:
--
--   (a) add ON UPDATE CASCADE to every FK in the content_id family, so updating
--       a parent PK (media_items / seasons / episodes) propagates to all FK
--       children automatically (the existing ON DELETE behaviour is preserved);
--       and
--   (b) provide silo_rename_content_id(from, to), which moves the PK rows plus
--       the unconstrained soft references (FK children follow via the cascade
--       from (a)). The soft-reference name predicate mirrors 20260612130000 so
--       the two stay in lockstep.
--
-- ON UPDATE CASCADE only fires when a content_id is updated (essentially never
-- outside this promotion), so there is no steady-state cost. The caller
-- (canonicalizeLocalContentID) guarantees the target id is free before calling
-- the function; a lost race surfaces as a unique violation and is retried.

-- +goose Up

-- (a) Rebuild every content_id-family FK with ON UPDATE CASCADE. pg_get_constraintdef
--     carries the existing ON DELETE clause through, so it is preserved.
-- +goose StatementBegin
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN
        SELECT con.conname,
               con.conrelid::regclass AS rel,
               pg_get_constraintdef(con.oid) AS condef
        FROM pg_constraint con
        WHERE con.contype = 'f'
          AND con.confrelid IN ('media_items'::regclass, 'seasons'::regclass, 'episodes'::regclass)
          AND position('ON UPDATE' IN pg_get_constraintdef(con.oid)) = 0
    LOOP
        EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', r.rel, r.conname);
        EXECUTE format('ALTER TABLE %s ADD CONSTRAINT %I %s ON UPDATE CASCADE',
                       r.rel, r.conname, r.condef);
    END LOOP;
END $$;
-- +goose StatementEnd

-- (b) Move one content_id value across the whole reference graph. FK children
--     follow via the cascade above; this updates the three PK columns and the
--     unconstrained soft references (same name predicate as 20260612130000).
--     The whole body runs as one statement, so the move is atomic.
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

    -- Array-valued soft references are not covered by the scalar loop above
    -- (text[] is not in the type filter and cannot carry an FK), matching the
    -- gap closed in 20260612130000 Step 6b. Negligible at runtime — a freshly
    -- matched local item is essentially never already in a trending snapshot —
    -- but kept in lockstep so a rename never leaves a stale array element.
    IF to_regclass('public.trending_discover_snapshots') IS NOT NULL THEN
        UPDATE trending_discover_snapshots
        SET content_ids = array_replace(content_ids, p_from, p_to)
        WHERE p_from = ANY(content_ids);
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose Down

DROP FUNCTION IF EXISTS silo_rename_content_id(text, text);

-- Revert the content_id-family FKs to their prior (no ON UPDATE) form.
-- +goose StatementBegin
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN
        SELECT con.conname,
               con.conrelid::regclass AS rel,
               pg_get_constraintdef(con.oid) AS condef
        FROM pg_constraint con
        WHERE con.contype = 'f'
          AND con.confrelid IN ('media_items'::regclass, 'seasons'::regclass, 'episodes'::regclass)
          AND position('ON UPDATE CASCADE' IN pg_get_constraintdef(con.oid)) > 0
    LOOP
        EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', r.rel, r.conname);
        EXECUTE format('ALTER TABLE %s ADD CONSTRAINT %I %s',
                       r.rel, r.conname,
                       replace(r.condef, ' ON UPDATE CASCADE', ''));
    END LOOP;
END $$;
-- +goose StatementEnd
