-- Content IDs are text COLLATE "C" (docs/architecture/deterministic-content-id.md),
-- and 20260612130000_deterministic_content_id moved every column that referenced
-- them. Ten tables created since then declared their reference as plain text, so
-- it took the database's default collation. A join from media_items.content_id
-- cannot use their content ID index across the two collations: a NOT EXISTS
-- against metadata_enrichment_state filtered a scan of the whole table for each
-- matched item, and the bulk enrichment pass's hourly probe took 15 minutes on a
-- catalog with 190,000 matched movies and series.
--
-- This moves every text column with a foreign key to media_items, seasons or
-- episodes whose collation differs from the column it references. Only the
-- collation changes: equality is the same, so no table is rewritten and the
-- foreign keys are not rechecked, but the indexes on these columns are rebuilt.
-- Each change also recreates the foreign key's triggers on the referenced table,
-- so the referenced tables (media_items on a stock schema) are locked against
-- reads and writes until the migration commits. They are locked first, so the
-- lock order is fixed. A 1 GB child table with 4 million rows took 4.5 seconds.
-- TestContentIDFamilyReferencesUseCCollationPostgres keeps new tables in line.

-- +goose Up
-- +goose StatementBegin
DO $$
DECLARE
    c RECORD;
    parents regclass[];
BEGIN
    CREATE TEMP TABLE _content_id_collation_targets ON COMMIT DROP AS
    SELECT DISTINCT con.conrelid::regclass AS table_name, a.attname AS column_name, con.confrelid::regclass AS parent
    FROM pg_constraint con
    CROSS JOIN LATERAL unnest(con.conkey, con.confkey) AS k(attnum, refnum)
    JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum
    JOIN pg_attribute r ON r.attrelid = con.confrelid AND r.attnum = k.refnum
    WHERE con.contype = 'f'
      AND con.confrelid IN ('public.media_items'::regclass, 'public.seasons'::regclass, 'public.episodes'::regclass)
      AND a.atttypid = 'text'::regtype
      AND a.attcollation <> r.attcollation;

    SELECT array_agg(DISTINCT parent ORDER BY parent) INTO parents FROM _content_id_collation_targets;
    IF parents IS NULL THEN
        RETURN;
    END IF;
    FOR c IN SELECT unnest(parents) AS parent LOOP
        EXECUTE format('LOCK TABLE %s IN ACCESS EXCLUSIVE MODE', c.parent);
    END LOOP;

    FOR c IN SELECT DISTINCT table_name, column_name FROM _content_id_collation_targets ORDER BY 1, 2 LOOP
        EXECUTE format('ALTER TABLE %s ALTER COLUMN %I TYPE text COLLATE "C"', c.table_name, c.column_name);
    END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
-- Returns the ten columns the Up moves on a stock schema to the default
-- collation they were created with.
-- +goose StatementBegin
ALTER TABLE public.audiobook_enrichment_state ALTER COLUMN content_id TYPE text COLLATE "default";
ALTER TABLE public.audiobook_item_file_stats ALTER COLUMN content_id TYPE text COLLATE "default";
ALTER TABLE public.item_videos ALTER COLUMN content_id TYPE text COLLATE "default";
ALTER TABLE public.local_sidecar_fingerprints ALTER COLUMN item_id TYPE text COLLATE "default";
ALTER TABLE public.media_extras ALTER COLUMN parent_id TYPE text COLLATE "default";
ALTER TABLE public.media_item_aliases ALTER COLUMN content_id TYPE text COLLATE "default";
ALTER TABLE public.media_item_backdrops ALTER COLUMN content_id TYPE text COLLATE "default";
ALTER TABLE public.media_item_rating_sources ALTER COLUMN content_id TYPE text COLLATE "default";
ALTER TABLE public.media_item_tmdb_collections ALTER COLUMN content_id TYPE text COLLATE "default";
ALTER TABLE public.metadata_enrichment_state ALTER COLUMN content_id TYPE text COLLATE "default";
-- +goose StatementEnd
