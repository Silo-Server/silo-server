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
-- reads and writes until the migration commits.
--
-- Running servers write to these tables while this runs, and a writer can hold
-- a child and then need its parent, or the reverse, so no fixed lock order
-- avoids a deadlock. The migration therefore takes every lock it needs up front,
-- gives each try 200ms, and on a miss releases them all and tries again. It never
-- waits long enough while holding a lock for a deadlock to form, and each try
-- queues reads of media_items for 200ms at most. When the locks are not
-- all free within 10 seconds, the migration fails rather than hold up reads;
-- startup then fails and the next start tries again. A 1 GB child table with 4
-- million rows took 4.5 seconds.
--
-- Changing a column's type drops its statistics, so the changed tables are
-- analyzed before the commit: the joins this fixes should not be planned from
-- default estimates until autovacuum gets to them.
-- TestContentIDFamilyReferencesUseCCollationPostgres keeps new tables in line.

-- +goose Up
-- +goose StatementBegin
DO $$
DECLARE
    c RECORD;
    parents regclass[];
    children regclass[];
    deadline timestamptz := clock_timestamp() + interval '10 seconds';
    try_until timestamptz;
    wait_ms integer;
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
    SELECT array_agg(DISTINCT table_name ORDER BY table_name) INTO children FROM _content_id_collation_targets;

    -- Take every lock the changes need before making any of them. One try lasts
    -- less than the default deadlock_timeout (1s), and a miss releases every lock
    -- it took, so a running writer that holds a child and then needs its parent
    -- never deadlocks with this.
    LOOP
        try_until := clock_timestamp() + interval '200 milliseconds';
        BEGIN
            FOR c IN SELECT unnest(parents || children) AS rel LOOP
                wait_ms := ceil(extract(epoch FROM try_until - clock_timestamp()) * 1000);
                IF wait_ms < 1 THEN
                    RAISE lock_not_available;
                END IF;
                PERFORM set_config('lock_timeout', wait_ms || 'ms', true);
                EXECUTE format('LOCK TABLE %s IN ACCESS EXCLUSIVE MODE', c.rel);
            END LOOP;
            EXIT;
        EXCEPTION WHEN lock_not_available OR deadlock_detected THEN
            IF clock_timestamp() >= deadline THEN
                RAISE;
            END IF;
        END;
        PERFORM pg_sleep(0.1);
    END LOOP;
    PERFORM set_config('lock_timeout', '10s', true);

    FOR c IN SELECT DISTINCT table_name, column_name FROM _content_id_collation_targets ORDER BY 1, 2 LOOP
        EXECUTE format('ALTER TABLE %s ALTER COLUMN %I TYPE text COLLATE "C"', c.table_name, c.column_name);
    END LOOP;
    FOR c IN SELECT DISTINCT table_name FROM _content_id_collation_targets ORDER BY 1 LOOP
        EXECUTE format('ANALYZE %s', c.table_name);
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
