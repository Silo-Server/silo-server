-- +goose Up
-- +goose StatementBegin
-- Convert every remaining serial-style id column to an identity column.
--
-- A `serial` column is not a type: it is an integer column plus a separate
-- sequence plus a DEFAULT nextval() pointing at it. The three pieces are only
-- conventionally related, so the sequence can be detached, dropped, re-pointed
-- or left behind without the column noticing. An identity column makes the
-- sequence an owned, inseparable part of the column definition, which is what
-- every table added since migration 005 already does (005_history_import.sql,
-- 139_media_requests.sql, 20260702172000_access_groups.sql). This migration
-- finishes the job for the tables that predate that convention, so the 1.0
-- baseline has exactly one way of generating a surrogate key.
--
-- The audit that produced the list, run against a scratch database with the
-- full chain applied -- a column is serial-style exactly when its default is a
-- nextval() on a sequence the column owns:
--
--   SELECT c.relname, a.attname
--   FROM pg_class c
--   JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
--   JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
--   JOIN pg_attrdef d ON d.adrelid = c.oid AND d.adnum = a.attnum
--   WHERE c.relkind IN ('r','p') AND NOT c.relispartition
--     AND pg_get_expr(d.adbin, d.adrelid) LIKE 'nextval%'
--   ORDER BY 1;
--
-- It returns the 32 tables below, every one of them on column `id`. Dropping
-- the NOT c.relispartition filter adds four more rows -- activity_log's
-- partitions, which hold a copy of the parent's default. ALTER TABLE on the
-- partitioned parent recurses to them, so they are not listed separately.
--
-- BY DEFAULT, not ALWAYS. ALWAYS rejects any INSERT that supplies the column
-- explicitly unless it says OVERRIDING SYSTEM VALUE, which is a behavior change
-- away from serial rather than a cleanup: internal/api/handlers/policy_test.go
-- inserts public.users (id, ...) with a fixed id today, and import/restore paths
-- that carry ids between databases would need the same escape hatch. BY DEFAULT
-- keeps serial's semantics exactly -- the sequence supplies the value when the
-- statement omits the column -- while still binding the sequence to the column.
-- Tables that were created as ALWAYS keep it; this migration does not touch them.
--
-- SEQUENCE CONTINUITY IS WHY THIS IS A DO BLOCK AND NOT 32 ALTER STATEMENTS.
-- Adding an identity column creates a *new* sequence that starts at 1. On a
-- database that has been running, ids 1..N already exist, so the next insert
-- collides with a live row: data corruption, not a failed deploy. For each
-- table the block therefore
--
--   1. drops the old default first, which takes the ACCESS EXCLUSIVE lock and
--      stops anyone else drawing from the old sequence;
--   2. reads last_value/is_called from the old sequence *after* that lock;
--   3. drops the old sequence, so the identity sequence can take back the
--      canonical <table>_id_seq name instead of being called <table>_id_seq1;
--   4. adds the identity column;
--   5. setval()s the new sequence to the captured position, carrying is_called
--      across so a never-used sequence still hands out its start value.
--
-- A column that is already an identity column is skipped, so an up/down/up
-- cycle or a re-run after a partial failure is a no-op rather than an error.
DO $$
DECLARE
    target text;
    seq text;
    last_val bigint;
    called boolean;
    identity_kind "char";
    targets text[] := ARRAY[
        'abs_sessions',
        'activity_log',
        'api_keys',
        'artwork_revision_gc_candidates',
        'autoscan_events',
        'autoscan_webhook_deliveries',
        'catalog_search_index_events',
        'download_artifact_orphans',
        'downloaded_subtitles',
        'invite_codes',
        'marker_edit_audit',
        'media_files',
        'media_folder_paths',
        'media_folders',
        'media_identity_overrides',
        'metadata_image_cache_jobs',
        'metadata_translation_jobs',
        'playback_route_events',
        'plugin_auth_bindings',
        'plugin_auth_identities',
        'plugin_capabilities',
        'plugin_installations',
        'plugin_repositories',
        'plugin_runtime_configs',
        'plugin_task_bindings',
        'stream_nodes',
        'subtitle_ai_jobs',
        'task_executions',
        'task_triggers',
        'user_setting_migration_rejects',
        'user_setting_values',
        'users'
    ];
BEGIN
    FOREACH target IN ARRAY targets LOOP
        SELECT a.attidentity INTO identity_kind
        FROM pg_attribute a
        JOIN pg_class c ON c.oid = a.attrelid
        JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
        WHERE c.relname = target AND a.attname = 'id' AND NOT a.attisdropped;

        IF identity_kind IS NULL THEN
            RAISE EXCEPTION 'serial-to-identity: public.%.id does not exist', target;
        END IF;
        IF identity_kind <> '' THEN
            CONTINUE;
        END IF;

        seq := pg_get_serial_sequence('public.' || quote_ident(target), 'id');
        IF seq IS NULL THEN
            RAISE EXCEPTION 'serial-to-identity: public.%.id owns no sequence', target;
        END IF;

        EXECUTE format('ALTER TABLE public.%I ALTER COLUMN id DROP DEFAULT', target);
        EXECUTE format('SELECT last_value, is_called FROM %s', seq) INTO last_val, called;
        EXECUTE format('DROP SEQUENCE %s', seq);
        EXECUTE format('ALTER TABLE public.%I ALTER COLUMN id ADD GENERATED BY DEFAULT AS IDENTITY', target);
        PERFORM setval(pg_get_serial_sequence('public.' || quote_ident(target), 'id'), last_val, called);
    END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Put the same 32 columns back on a plain sequence with a nextval() default,
-- carrying the identity sequence's position across exactly as the Up carried
-- the serial sequence's position forward: DROP IDENTITY destroys the sequence
-- with the column definition, so the position is read before it runs.
--
-- The recreated sequence keeps the identity sequence's name -- which is the
-- original <table>_id_seq, because the Up freed that name before adding the
-- identity -- and takes its data type from the column, so an integer id gets an
-- integer sequence with the integer ceiling, exactly as `serial` built it.
DO $$
DECLARE
    target text;
    seq text;
    seq_name text;
    col_type text;
    last_val bigint;
    called boolean;
    identity_kind "char";
    targets text[] := ARRAY[
        'abs_sessions',
        'activity_log',
        'api_keys',
        'artwork_revision_gc_candidates',
        'autoscan_events',
        'autoscan_webhook_deliveries',
        'catalog_search_index_events',
        'download_artifact_orphans',
        'downloaded_subtitles',
        'invite_codes',
        'marker_edit_audit',
        'media_files',
        'media_folder_paths',
        'media_folders',
        'media_identity_overrides',
        'metadata_image_cache_jobs',
        'metadata_translation_jobs',
        'playback_route_events',
        'plugin_auth_bindings',
        'plugin_auth_identities',
        'plugin_capabilities',
        'plugin_installations',
        'plugin_repositories',
        'plugin_runtime_configs',
        'plugin_task_bindings',
        'stream_nodes',
        'subtitle_ai_jobs',
        'task_executions',
        'task_triggers',
        'user_setting_migration_rejects',
        'user_setting_values',
        'users'
    ];
BEGIN
    FOREACH target IN ARRAY targets LOOP
        SELECT a.attidentity, format_type(a.atttypid, a.atttypmod)
        INTO identity_kind, col_type
        FROM pg_attribute a
        JOIN pg_class c ON c.oid = a.attrelid
        JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
        WHERE c.relname = target AND a.attname = 'id' AND NOT a.attisdropped;

        IF identity_kind IS NULL THEN
            RAISE EXCEPTION 'identity-to-serial: public.%.id does not exist', target;
        END IF;
        IF identity_kind = '' THEN
            CONTINUE;
        END IF;

        seq := pg_get_serial_sequence('public.' || quote_ident(target), 'id');
        SELECT c.relname INTO seq_name FROM pg_class c WHERE c.oid = seq::regclass;
        -- Lock before reading the position so a concurrent insert cannot advance the
        -- sequence between the read and DROP IDENTITY (the Up gets this lock from DROP DEFAULT).
        EXECUTE format('LOCK TABLE public.%I IN ACCESS EXCLUSIVE MODE', target);
        EXECUTE format('SELECT last_value, is_called FROM %s', seq) INTO last_val, called;

        EXECUTE format('ALTER TABLE public.%I ALTER COLUMN id DROP IDENTITY', target);
        EXECUTE format('CREATE SEQUENCE public.%I AS %s OWNED BY public.%I.id',
                       seq_name, col_type, target);
        PERFORM setval(format('public.%I', seq_name)::regclass, last_val, called);
        EXECUTE format('ALTER TABLE public.%I ALTER COLUMN id SET DEFAULT nextval(%L)',
                       target, format('public.%I', seq_name));
    END LOOP;
END
$$;
-- +goose StatementEnd
