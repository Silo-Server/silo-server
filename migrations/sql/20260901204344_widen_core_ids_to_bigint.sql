-- +goose Up
-- +goose StatementBegin
-- Widen users.id, media_files.id and media_folders.id from integer to bigint.
--
-- These three are the schema's oldest surrogate keys and the only integer
-- parents that newer tables already reference with bigint columns:
-- invitations.invited_by / accepted_user_id, invite_codes.created_by,
-- playback_route_events.user_id, playback_v3_attempts.user_id /
-- requested_media_file_id / effective_media_file_id, and
-- page_sections.library_id. Widening the parents makes the type honest in one
-- direction -- a bigint child can no longer hold a value the parent cannot --
-- and gives the 1.0 baseline a 64-bit ceiling on the three tables whose ids are
-- minted most often (every scanned file draws one).
--
-- WHAT THIS DOES ON THE SERVER. integer -> bigint is not binary-coercible, so
-- each ALTER rewrites the table and rebuilds every index on it, holding
-- ACCESS EXCLUSIVE on the table for the whole copy. Verified on a scratch
-- database (PostgreSQL 18): pg_class.relfilenode changes for the table and for
-- all of its indexes. The cost is proportional to rows x row width, which
-- makes media_files -- the widest table in the schema, one row per scanned
-- file -- the one that decides the window; users and media_folders are small
-- on every install. The rewrite also needs free disk for a second copy of the
-- table and its indexes while it runs.
--
-- Postgres additionally drops and re-adds every foreign key that references
-- the altered column, which takes ACCESS EXCLUSIVE on each referencing table
-- as well and revalidates each constraint with one sequential scan of the
-- child. Verified on the same scratch database: the users ALTER holds ACCESS
-- EXCLUSIVE on every relation (table or partition) that references users --
-- the count varies by install because activity_log and operational_logs are
-- date-partitioned -- and pg_stat_user_tables shows seq_scan = 1 /
-- seq_tup_read = <row count> on each child (5000 seeded user_profiles rows
-- read once), while every child's relfilenode is unchanged -- read, never
-- rewritten. So the window is the three rewrites plus one pass over every
-- referencing table, and nothing can read or write any of those tables until
-- the transaction commits. Treat it as a full-schema outage, not a
-- three-table one.
--
-- ALTER COLUMN ... TYPE on an identity column retypes the owned sequence in
-- the same statement: pg_sequences.data_type goes integer -> bigint and
-- max_value 2147483647 -> 9223372036854775807, without an explicit ALTER
-- SEQUENCE. Verified on the scratch database before and after the users ALTER.
--
-- CHILDREN ARE NOT WIDENED HERE. Postgres allows an integer column to
-- reference a bigint key, so the parents can move alone. The integer children
-- stay behind deliberately: each one is its own table rewrite, and the useful
-- ones -- the wide, per-file and per-event tables -- deserve their own window.
-- The practical consequence is that, until a child is widened, a parent id
-- above 2147483647 cannot be written into that child at all (integer out of
-- range), so the effective ceiling of users.id / media_folders.id /
-- media_files.id stays at int4 for any row an integer child must reference.
-- The audit on the scratch database, excluding partitions (they follow their
-- parent):
--
--   users -> 85 integer FK columns, e.g. user_profiles, auth_sessions,
--     api_keys, user_watch_progress, user_watch_history, user_favorites,
--     activity_log (partitioned), operational_logs (partitioned),
--     playback_sessions_sync, downloads, media_requests, push_devices,
--     web_push_subscriptions, notification_*, user_setting_values,
--     user_setting_mutations, admin_playback_history. Most are per-user,
--     per-profile or per-device rows and rewrite in seconds; the per-event
--     ones -- activity_log, operational_logs, user_watch_history,
--     admin_playback_history, playback_sessions_sync, user_setting_mutations
--     -- grow with usage and are the costly ones.
--   media_folders -> 29 integer FK columns. Cheap except media_files.
--     media_folder_id (the same rewrite as this migration's media_files
--     ALTER; bundling it into this statement would have cost nothing extra),
--     episode_catalog_entries.media_folder_id and media_item_libraries.
--     media_folder_id (per-episode / per-item), episode_libraries.
--     media_folder_id, and the scanner state tables scanned_media_roots /
--     scanned_media_groups / observed_media_locations / media_group_locations
--     / media_item_roots / media_item_groups (per-directory). Two plpgsql
--     functions take a media_folder_id integer parameter --
--     refresh_episode_catalog_entry(text, integer) and
--     refresh_audiobook_item_file_stats(integer, text) -- and are called with
--     child columns; widening those children needs the functions retyped in
--     the same migration or the trigger calls stop resolving.
--   media_files -> 9 integer FK columns: abs_playback_sessions,
--     download_artifacts, downloaded_subtitles, downloads,
--     ebook_reader_progress.file_id, marker_contributions, marker_edit_audit,
--     media_intro_fingerprints, movie_match_queue. All small next to
--     media_files itself.
--
-- Beyond the FKs, a number of plain integer columns hold one of these ids
-- without a constraint -- users.library_ids integer[], access_groups.
-- library_ids, invitations.library_ids, the *_availability.library_id
-- columns, user_watch_progress.last_file_id, playback_sessions_sync.
-- media_file_id, and others found by grepping for *_id / *_ids integer
-- columns; they carry the same int4 ceiling and belong in the same child
-- sweep.
--
-- Four advisory-lock sites in Go also narrow users.id to int4 and belong in
-- that sweep, not here: internal/userstore/pgstore/preference_settings_tx.go
-- (Go int32(s.userID), which wraps silently so two accounts 2^32 apart would
-- share a lock key) and the $2::int4 casts in internal/requests/repository.go,
-- internal/diagnostics/repo.go and internal/subtitles/ai/pgrepo.go (which
-- error with "integer out of range"). All are unreachable today because every
-- integer child of users blocks such an account from existing. When the
-- children widen, switch each to the single-bigint form
-- pg_advisory_xact_lock($1::bigint) with the namespace folded into the key
-- (e.g. hashtext(namespace) or namespace << 32 | user_id).
--
-- The three tables are altered in one transaction (goose's default), so a
-- failure part-way leaves all three integer. Order is smallest first so a
-- disk-space or lock-timeout failure surfaces before the media_files copy.
ALTER TABLE public.media_folders ALTER COLUMN id TYPE bigint;
ALTER TABLE public.users ALTER COLUMN id TYPE bigint;
ALTER TABLE public.media_files ALTER COLUMN id TYPE bigint;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Narrow the three columns back to integer -- the same rewrite in the other
-- direction, with the same locks -- but only if every stored id and every
-- sequence position still fits. A value outside the integer range (above
-- 2147483647 or below -2147483648; the identity column is BY DEFAULT, so a
-- negative id can be inserted explicitly) is refused with a message naming
-- the table and the value, before any ALTER runs, so a rollback can never
-- truncate an id or leave a sequence that cannot be retyped. (Postgres would
-- also reject the ALTER itself with "integer out of range" / "RESTART value
-- cannot be greater than MAXVALUE", but only after starting the copy and
-- without saying which row.) Children are not touched: an integer child
-- referencing an integer parent is where the Up started, and the bigint
-- children were bigint before this migration.
DO $$
DECLARE
    target text;
    min_id bigint;
    max_id bigint;
    seq_pos bigint;
    targets text[] := ARRAY['media_folders', 'users', 'media_files'];
BEGIN
    FOREACH target IN ARRAY targets LOOP
        EXECUTE format('SELECT min(id), max(id) FROM public.%I', target) INTO min_id, max_id;
        IF max_id > 2147483647 THEN
            RAISE EXCEPTION 'widen_core_ids_to_bigint down: public.%.id holds % which does not fit integer; remove or renumber the row before rolling back',
                target, max_id;
        END IF;
        IF min_id < -2147483648 THEN
            RAISE EXCEPTION 'widen_core_ids_to_bigint down: public.%.id holds % which does not fit integer; remove or renumber the row before rolling back',
                target, min_id;
        END IF;
        EXECUTE format('SELECT last_value FROM %s', pg_get_serial_sequence('public.' || quote_ident(target), 'id')) INTO seq_pos;
        IF seq_pos > 2147483647 THEN
            RAISE EXCEPTION 'widen_core_ids_to_bigint down: public.%_id_seq is at % which does not fit integer; setval it below 2147483647 before rolling back',
                target, seq_pos;
        END IF;
    END LOOP;
END
$$;

ALTER TABLE public.media_files ALTER COLUMN id TYPE integer;
ALTER TABLE public.users ALTER COLUMN id TYPE integer;
ALTER TABLE public.media_folders ALTER COLUMN id TYPE integer;
-- +goose StatementEnd
