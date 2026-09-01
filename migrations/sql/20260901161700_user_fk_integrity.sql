-- +goose Up
-- +goose StatementBegin
-- Constrain the user references that were never declared as foreign keys.
--
-- auth.UserRepository.Delete is a bare "DELETE FROM users WHERE id = $1": every
-- row that survives a user deletion survives because a foreign key said so. A
-- user column with no constraint therefore leaks rows on every account deletion
-- -- push tokens that keep receiving notifications, cached recommendations,
-- notification preferences, live Jellyfin compat sessions -- and nothing ever
-- collects them, because nothing knows they are garbage.
--
-- The list was derived on a scratch database with the full chain applied:
--
--   WITH fk_cols AS (
--     SELECT con.conrelid AS relid, unnest(con.conkey) AS attnum
--     FROM pg_constraint con WHERE con.contype = 'f'
--   )
--   SELECT c.relname, a.attname, format_type(a.atttypid, a.atttypmod), a.attnotnull
--   FROM pg_class c
--   JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
--   JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
--   WHERE c.relkind IN ('r', 'p')
--     AND a.attname ~ '(^|_)(user_id|created_by|updated_by|requested_by|actor|owner)'
--     AND NOT EXISTS (
--       SELECT 1 FROM fk_cols f WHERE f.relid = c.oid AND f.attnum = a.attnum
--     );
--
-- and then read against the code that owns each table. Three groups of
-- candidates are deliberately left alone:
--
--   * Columns holding a foreign system's identity rather than a users.id:
--     history_import_*.connect_user_id / external_user_id,
--     webhook_sync_*.external_user_id, notification_discord_prefs.discord_user_id,
--     jellycompat_sessions.pseudo_user_id. Also jellycompat_playback_sessions.user_id
--     and oauth_session.linking_user_id, which do carry a Silo user id but store
--     it as text (the latter is strconv.Atoi'd in internal/auth/oauth_handler.go);
--     constraining those means retyping the column, which is not this change.
--   * policy_decisions.user_id and its partitions. Exempt on purpose: it is the
--     policy audit trail, written on the hottest evaluation path and aged out by
--     dropping partitions. A parent lookup per inserted row and a rewrite of
--     history on every account deletion both cost more than the constraint is
--     worth, and the row is evidence of what was decided about a user -- it
--     stays true after the account is gone.
--   * notification_server_channels.created_by_user_id. Migration
--     20260612020209 states the intent in the table's own comment: no FK, the
--     column is informational. A server-wide notification channel is server
--     configuration that must outlive the admin who created it, and the column
--     is NOT NULL, so neither CASCADE (deletes the channel) nor SET NULL (not
--     permitted) expresses that. Left alone.
--
-- ON DELETE follows the existing shape of this schema (56 CASCADE, 16 SET NULL,
-- every SET NULL on a nullable column) and the meaning of each row: CASCADE
-- where the row belongs to the account and is meaningless without it, SET NULL
-- where the row records something that happened and the user is attribution.
--
-- Orphans. An upgraded database can already hold rows whose user_id no longer
-- exists -- that is the leak being closed -- and ADD CONSTRAINT refuses to
-- validate against them. Each constraint is therefore preceded by a statement
-- scoped to exactly those rows: a targeted DELETE where the column is NOT NULL
-- (the row belongs to an account that no longer exists and can never be reached
-- again), or an UPDATE ... SET NULL where the column is nullable by meaning and
-- the row is a record worth keeping. Nothing else is touched, and neither is
-- reversible; see the Down section.
--
-- The migration runs in one transaction, so it is all-or-nothing, at the cost of
-- holding the ALTER TABLE locks on all seventeen child tables and on users for
-- its duration. ADD CONSTRAINT validates by scanning the child table and blocks
-- writes to both sides while it does, so on a large deployment this belongs in
-- the same maintenance window as any other table-scanning schema change.

-- 1) User-owned rows: the row exists only to serve one account. CASCADE.

-- push_devices: APNs/FCM tokens for one account's devices. An orphan is a live
-- push route to a deleted account.
DELETE FROM public.push_devices t
WHERE NOT EXISTS (SELECT 1 FROM public.users u WHERE u.id = t.user_id);

ALTER TABLE public.push_devices
    ADD CONSTRAINT push_devices_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

-- web_push_subscriptions: the same, for browser push endpoints.
DELETE FROM public.web_push_subscriptions t
WHERE NOT EXISTS (SELECT 1 FROM public.users u WHERE u.id = t.user_id);

ALTER TABLE public.web_push_subscriptions
    ADD CONSTRAINT web_push_subscriptions_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

-- notification_webhooks: outbound webhooks owned by one account's profiles.
DELETE FROM public.notification_webhooks t
WHERE NOT EXISTS (SELECT 1 FROM public.users u WHERE u.id = t.user_id);

ALTER TABLE public.notification_webhooks
    ADD CONSTRAINT notification_webhooks_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

-- notification_email_prefs: one account's email notification settings.
DELETE FROM public.notification_email_prefs t
WHERE NOT EXISTS (SELECT 1 FROM public.users u WHERE u.id = t.user_id);

ALTER TABLE public.notification_email_prefs
    ADD CONSTRAINT notification_email_prefs_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

-- notification_discord_prefs: one account's Discord notification settings;
-- user_id is the primary key.
DELETE FROM public.notification_discord_prefs t
WHERE NOT EXISTS (SELECT 1 FROM public.users u WHERE u.id = t.user_id);

ALTER TABLE public.notification_discord_prefs
    ADD CONSTRAINT notification_discord_prefs_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

-- notification_discord_link_state: an in-flight Discord account-link handshake.
DELETE FROM public.notification_discord_link_state t
WHERE NOT EXISTS (SELECT 1 FROM public.users u WHERE u.id = t.user_id);

ALTER TABLE public.notification_discord_link_state
    ADD CONSTRAINT notification_discord_link_state_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

-- notification_deliveries: the per-profile notification inbox.
DELETE FROM public.notification_deliveries t
WHERE NOT EXISTS (SELECT 1 FROM public.users u WHERE u.id = t.user_id);

ALTER TABLE public.notification_deliveries
    ADD CONSTRAINT notification_deliveries_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

-- profile_series_interest: which series a profile follows, which decides who
-- receives a new-episode notification.
DELETE FROM public.profile_series_interest t
WHERE NOT EXISTS (SELECT 1 FROM public.users u WHERE u.id = t.user_id);

ALTER TABLE public.profile_series_interest
    ADD CONSTRAINT profile_series_interest_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

-- user_audio_preferences: per user/profile/series audio track choice. Its
-- neighbours user_subtitle_preferences and user_series_playback_preferences
-- already CASCADE.
DELETE FROM public.user_audio_preferences t
WHERE NOT EXISTS (SELECT 1 FROM public.users u WHERE u.id = t.user_id);

ALTER TABLE public.user_audio_preferences
    ADD CONSTRAINT user_audio_preferences_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

-- recommendation_cache: derived per-user recommendations, rebuildable.
DELETE FROM public.recommendation_cache t
WHERE NOT EXISTS (SELECT 1 FROM public.users u WHERE u.id = t.user_id);

ALTER TABLE public.recommendation_cache
    ADD CONSTRAINT recommendation_cache_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

-- playback_sessions_sync: durable playback session state keyed to the account
-- playing. Its admin-history sibling playback_history_admin.user_id already
-- CASCADEs.
DELETE FROM public.playback_sessions_sync t
WHERE NOT EXISTS (SELECT 1 FROM public.users u WHERE u.id = t.user_id);

ALTER TABLE public.playback_sessions_sync
    ADD CONSTRAINT playback_sessions_sync_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

-- jellycompat_sessions.streamapp_user_id: the Silo account behind a Jellyfin
-- compat session. The row carries that session's token, so it must not outlive
-- the account. (pseudo_user_id on the same table is the synthetic id handed to
-- the Jellyfin client and is deliberately not a reference.)
DELETE FROM public.jellycompat_sessions t
WHERE NOT EXISTS (SELECT 1 FROM public.users u WHERE u.id = t.streamapp_user_id);

ALTER TABLE public.jellycompat_sessions
    ADD CONSTRAINT jellycompat_sessions_streamapp_user_id_fkey FOREIGN KEY (streamapp_user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

-- admin_jobs.created_by_user_id: not merely informational -- canReadAdminJob in
-- internal/api/handlers/admin_jobs.go compares it against the caller to decide
-- who may read an item-refresh job. Jobs are ephemeral (they carry expires_at
-- and are swept), so a job whose requesting admin is gone goes with them.
DELETE FROM public.admin_jobs t
WHERE NOT EXISTS (SELECT 1 FROM public.users u WHERE u.id = t.created_by_user_id);

ALTER TABLE public.admin_jobs
    ADD CONSTRAINT admin_jobs_created_by_user_id_fkey FOREIGN KEY (created_by_user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

-- auth_sessions.impersonator_user_id: nullable, but CASCADE rather than SET NULL
-- on purpose. The column is what makes the session an impersonation; nulling it
-- would silently turn a live admin-impersonation session into an ordinary
-- session for the impersonated user. Once the impersonating admin's account is
-- deleted the authority behind the session is gone and the session must go with
-- it -- the same thing SessionRepository.RevokeAllByImpersonator does
-- explicitly. The orphan sweep removes those rows for the same reason.
DELETE FROM public.auth_sessions t
WHERE t.impersonator_user_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM public.users u WHERE u.id = t.impersonator_user_id);

ALTER TABLE public.auth_sessions
    ADD CONSTRAINT auth_sessions_impersonator_user_id_fkey FOREIGN KEY (impersonator_user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

-- 2) Attribution on a record that outlives the account: nullable columns, SET
-- NULL, and an orphan sweep that nulls instead of deleting.

-- activity_log.impersonator_user_id: the request audit log. Its sibling
-- activity_log.user_id has carried ON DELETE SET NULL since the 001 baseline;
-- impersonator_user_id was added later without one, so the same table enforces
-- one of its two user references and not the other. This is a partitioned table:
-- the constraint is declared on the parent and Postgres applies it to every
-- existing partition and to future ones, including the partitions
-- internal/partman creates and ATTACHes. The existing partial index
-- idx_activity_log_impersonator_user_id (WHERE impersonator_user_id IS NOT NULL)
-- covers exactly the rows a SET NULL action visits, so both the sweep below and
-- the action itself are index-driven.
UPDATE public.activity_log t
SET impersonator_user_id = NULL
WHERE t.impersonator_user_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM public.users u WHERE u.id = t.impersonator_user_id);

ALTER TABLE public.activity_log
    ADD CONSTRAINT activity_log_impersonator_user_id_fkey FOREIGN KEY (impersonator_user_id)
    REFERENCES public.users(id) ON DELETE SET NULL;

-- subtitle_ai_jobs.requested_by: nullable because a job can be system-initiated.
-- It drives the per-user quota window in internal/subtitles/ai/pgrepo.go, but
-- the job row and its output stay useful once the requester is gone.
UPDATE public.subtitle_ai_jobs t
SET requested_by = NULL
WHERE t.requested_by IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM public.users u WHERE u.id = t.requested_by);

ALTER TABLE public.subtitle_ai_jobs
    ADD CONSTRAINT subtitle_ai_jobs_requested_by_fkey FOREIGN KEY (requested_by)
    REFERENCES public.users(id) ON DELETE SET NULL;

-- metadata_translation_jobs.requested_by: the same shape as subtitle_ai_jobs.
UPDATE public.metadata_translation_jobs t
SET requested_by = NULL
WHERE t.requested_by IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM public.users u WHERE u.id = t.requested_by);

ALTER TABLE public.metadata_translation_jobs
    ADD CONSTRAINT metadata_translation_jobs_requested_by_fkey FOREIGN KEY (requested_by)
    REFERENCES public.users(id) ON DELETE SET NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Drop the seventeen constraints. The columns keep their values and their types;
-- only the enforcement goes away.
--
-- Lossy in reverse, deliberately: the Up deleted the rows that pointed at
-- accounts which no longer exist and nulled the attribution columns that did the
-- same, and archived neither. Rolling back restores the schema's ability to hold
-- such rows, not the rows themselves. Restore from a backup if the orphans
-- mattered.
ALTER TABLE public.metadata_translation_jobs
    DROP CONSTRAINT IF EXISTS metadata_translation_jobs_requested_by_fkey;

ALTER TABLE public.subtitle_ai_jobs
    DROP CONSTRAINT IF EXISTS subtitle_ai_jobs_requested_by_fkey;

ALTER TABLE public.activity_log
    DROP CONSTRAINT IF EXISTS activity_log_impersonator_user_id_fkey;

ALTER TABLE public.auth_sessions
    DROP CONSTRAINT IF EXISTS auth_sessions_impersonator_user_id_fkey;

ALTER TABLE public.admin_jobs
    DROP CONSTRAINT IF EXISTS admin_jobs_created_by_user_id_fkey;

ALTER TABLE public.jellycompat_sessions
    DROP CONSTRAINT IF EXISTS jellycompat_sessions_streamapp_user_id_fkey;

ALTER TABLE public.playback_sessions_sync
    DROP CONSTRAINT IF EXISTS playback_sessions_sync_user_id_fkey;

ALTER TABLE public.recommendation_cache
    DROP CONSTRAINT IF EXISTS recommendation_cache_user_id_fkey;

ALTER TABLE public.user_audio_preferences
    DROP CONSTRAINT IF EXISTS user_audio_preferences_user_id_fkey;

ALTER TABLE public.profile_series_interest
    DROP CONSTRAINT IF EXISTS profile_series_interest_user_id_fkey;

ALTER TABLE public.notification_deliveries
    DROP CONSTRAINT IF EXISTS notification_deliveries_user_id_fkey;

ALTER TABLE public.notification_discord_link_state
    DROP CONSTRAINT IF EXISTS notification_discord_link_state_user_id_fkey;

ALTER TABLE public.notification_discord_prefs
    DROP CONSTRAINT IF EXISTS notification_discord_prefs_user_id_fkey;

ALTER TABLE public.notification_email_prefs
    DROP CONSTRAINT IF EXISTS notification_email_prefs_user_id_fkey;

ALTER TABLE public.notification_webhooks
    DROP CONSTRAINT IF EXISTS notification_webhooks_user_id_fkey;

ALTER TABLE public.web_push_subscriptions
    DROP CONSTRAINT IF EXISTS web_push_subscriptions_user_id_fkey;

ALTER TABLE public.push_devices
    DROP CONSTRAINT IF EXISTS push_devices_user_id_fkey;
-- +goose StatementEnd
