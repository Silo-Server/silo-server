-- +goose Up
-- +goose StatementBegin
-- Stage 3 of schema hygiene follows the dead-table and integrity migrations.
-- Stop all API/integrated replicas before Up or Down: old SQL names cease to
-- exist atomically at commit. Resume only binaries matching the resulting schema.
-- OAuth completion encryption uses a fixed key label and code_hash AAD, neither
-- of which changes. No server_settings keys or public API identifiers are renamed.
-- RENAME INDEX also renames a backing primary-key/unique constraint.
ALTER TABLE public.oauth_session RENAME TO oauth_sessions;
ALTER INDEX public.oauth_session_pkey RENAME TO oauth_sessions_pkey;
ALTER INDEX public.oauth_session_expires_idx RENAME TO oauth_sessions_expires_idx;

ALTER TABLE public.oauth_completion RENAME TO oauth_completions;
ALTER INDEX public.oauth_completion_pkey RENAME TO oauth_completions_pkey;
ALTER INDEX public.oauth_completion_expires_idx RENAME TO oauth_completions_expires_idx;

ALTER TABLE public.playback_history_admin RENAME TO admin_playback_history;
ALTER INDEX public.playback_history_admin_pkey RENAME TO admin_playback_history_pkey;
ALTER INDEX public.idx_playback_history_admin_ended RENAME TO idx_admin_playback_history_ended;
ALTER INDEX public.idx_playback_history_admin_started RENAME TO idx_admin_playback_history_started;
ALTER INDEX public.idx_playback_history_admin_user_ended RENAME TO idx_admin_playback_history_user_ended;
ALTER INDEX public.idx_playback_history_admin_user_profile_ended RENAME TO idx_admin_playback_history_user_profile_ended;
ALTER TABLE public.admin_playback_history
    RENAME CONSTRAINT playback_history_admin_user_id_fkey TO admin_playback_history_user_id_fkey;

-- Postgres 18 catalogs every NOT NULL as a named constraint
-- (<table>_<column>_not_null) that RENAME TABLE also leaves behind; older
-- servers have no such rows, so this is a lookup rather than a fixed list.
DO $$
DECLARE
    pair record;
    con record;
BEGIN
    FOR pair IN SELECT * FROM (VALUES
        ('oauth_session', 'oauth_sessions'),
        ('oauth_completion', 'oauth_completions'),
        ('playback_history_admin', 'admin_playback_history')
    ) AS t(old_name, new_name) LOOP
        FOR con IN
            SELECT conname FROM pg_constraint
            WHERE conrelid = format('public.%I', pair.new_name)::regclass
              AND contype = 'n'
              AND starts_with(conname, pair.old_name || '_')
        LOOP
            EXECUTE format('ALTER TABLE public.%I RENAME CONSTRAINT %I TO %I',
                           pair.new_name, con.conname,
                           pair.new_name || substr(con.conname, length(pair.old_name) + 1));
        END LOOP;
    END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Rename everything back, in the reverse order, so an older binary that still
-- writes the old names works again. No data moves in either direction.
DO $$
DECLARE
    pair record;
    con record;
BEGIN
    FOR pair IN SELECT * FROM (VALUES
        ('oauth_sessions', 'oauth_session'),
        ('oauth_completions', 'oauth_completion'),
        ('admin_playback_history', 'playback_history_admin')
    ) AS t(old_name, new_name) LOOP
        FOR con IN
            SELECT conname FROM pg_constraint
            WHERE conrelid = format('public.%I', pair.old_name)::regclass
              AND contype = 'n'
              AND starts_with(conname, pair.old_name || '_')
        LOOP
            EXECUTE format('ALTER TABLE public.%I RENAME CONSTRAINT %I TO %I',
                           pair.old_name, con.conname,
                           pair.new_name || substr(con.conname, length(pair.old_name) + 1));
        END LOOP;
    END LOOP;
END
$$;

ALTER TABLE public.admin_playback_history
    RENAME CONSTRAINT admin_playback_history_user_id_fkey TO playback_history_admin_user_id_fkey;
ALTER INDEX public.idx_admin_playback_history_user_profile_ended RENAME TO idx_playback_history_admin_user_profile_ended;
ALTER INDEX public.idx_admin_playback_history_user_ended RENAME TO idx_playback_history_admin_user_ended;
ALTER INDEX public.idx_admin_playback_history_started RENAME TO idx_playback_history_admin_started;
ALTER INDEX public.idx_admin_playback_history_ended RENAME TO idx_playback_history_admin_ended;
ALTER INDEX public.admin_playback_history_pkey RENAME TO playback_history_admin_pkey;
ALTER TABLE public.admin_playback_history RENAME TO playback_history_admin;

ALTER INDEX public.oauth_completions_expires_idx RENAME TO oauth_completion_expires_idx;
ALTER INDEX public.oauth_completions_pkey RENAME TO oauth_completion_pkey;
ALTER TABLE public.oauth_completions RENAME TO oauth_completion;

ALTER INDEX public.oauth_sessions_expires_idx RENAME TO oauth_session_expires_idx;
ALTER INDEX public.oauth_sessions_pkey RENAME TO oauth_session_pkey;
ALTER TABLE public.oauth_sessions RENAME TO oauth_session;
-- +goose StatementEnd
