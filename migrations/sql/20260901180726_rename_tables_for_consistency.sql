-- +goose Up
-- +goose StatementBegin
-- Rename three tables that break the schema's own naming rules, while renaming
-- is still free (pre-1.0, no external consumer reads table names).
--
--   oauth_session          -> oauth_sessions
--   oauth_completion       -> oauth_completions
--   playback_history_admin -> admin_playback_history
--
-- The first two name one row in the singular while every table that holds the
-- same kind of short-lived row is plural: auth_sessions, jellycompat_sessions,
-- watch_provider_auth_sessions, device_login_requests. The third puts its scope
-- qualifier in the wrong place: everything else leads with the scope -- admin_jobs,
-- admin_dashboard_layouts, and the whole user_* family, including the sibling
-- user_watch_history this table shadows at server scope. `history` stays a mass
-- noun there, so admin_playback_history needs no plural.
--
-- SAFE TO RENAME -- the encryption check, done before writing this migration:
-- Silo binds some encrypted values to an identifier through AES-GCM additional
-- authenticated data, and a rename that changes that identifier makes the
-- values permanently undecryptable. The two places that do it are
-- secret.RowAAD(table, column, pk) -- whose 16 call sites are in jellycompat,
-- webhooksync, autoscan, plugins, historyimport, requests, watchsync, subtitles
-- and notifications, and name none of these three tables -- and the encrypted
-- server_settings rows, which bind to the setting key and are untouched here.
-- Of the three tables only oauth_completion stores a secret,
-- token_ciphertext, and internal/auth/oauth_store.go seals it with a key
-- derived from sha256("silo/oauth-completion/v1:" + secret) and AAD equal to
-- the row's code_hash. Neither input mentions the table, so existing rows
-- decrypt unchanged after the rename. (They are also single-use and expire in
-- minutes.)
--
-- ALTER TABLE ... RENAME does not rename the indexes and constraints that were
-- named after the table, so each one is renamed explicitly; leaving them would
-- trade one inconsistency for a worse one.
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
              AND conname LIKE pair.old_name || '\_%'
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
              AND conname LIKE pair.old_name || '\_%'
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
