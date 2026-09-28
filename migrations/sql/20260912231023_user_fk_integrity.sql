-- +goose Up
-- Schema hygiene stage 2; follows the apiv2 replacement of PR #879.
-- Account-owned state cascades. Attribution survives with NULL. Impersonation
-- sessions cascade when their issuing admin is deleted: SET NULL would turn
-- them into ordinary sessions for the impersonated account.
--
-- Orphan references are remnants of deleted accounts. Account-owned rows are
-- removed and historical attribution is cleared before constraints are added.
-- Validation scans and ALTER TABLE locks block writes for the transaction's
-- duration; schedule a maintenance window and budget the migration timeout.
--
-- Exclusions: policy_decisions is historical evidence; notification server
-- channel creator IDs are informational. External identities and the text user
-- IDs in oauth_session/jellycompat_playback_sessions need separate type work.
-- +goose StatementBegin
DO $$
DECLARE
    reference record;
    orphan_count bigint;
BEGIN
    FOR reference IN SELECT * FROM (VALUES
        ('push_devices', 'user_id'),
        ('web_push_subscriptions', 'user_id'),
        ('notification_webhooks', 'user_id'),
        ('notification_email_prefs', 'user_id'),
        ('notification_discord_prefs', 'user_id'),
        ('notification_discord_link_state', 'user_id'),
        ('notification_deliveries', 'user_id'),
        ('profile_series_interest', 'user_id'),
        ('user_audio_preferences', 'user_id'),
        ('recommendation_cache', 'user_id'),
        ('playback_sessions_sync', 'user_id'),
        ('jellycompat_sessions', 'streamapp_user_id'),
        ('admin_jobs', 'created_by_user_id'),
        ('auth_sessions', 'impersonator_user_id'),
        ('activity_log', 'impersonator_user_id'),
        ('subtitle_ai_jobs', 'requested_by'),
        ('metadata_translation_jobs', 'requested_by')
    ) AS refs(table_name, column_name)
    LOOP
        EXECUTE format('SELECT count(*) FROM public.%I child WHERE child.%I IS NOT NULL AND NOT EXISTS (SELECT 1 FROM public.users parent WHERE parent.id = child.%I)',
            reference.table_name, reference.column_name, reference.column_name)
            INTO orphan_count;
        IF orphan_count > 0 THEN
            IF reference.table_name IN ('activity_log', 'subtitle_ai_jobs', 'metadata_translation_jobs') THEN
                EXECUTE format('UPDATE public.%I child SET %I = NULL WHERE child.%I IS NOT NULL AND NOT EXISTS (SELECT 1 FROM public.users parent WHERE parent.id = child.%I)', reference.table_name, reference.column_name, reference.column_name, reference.column_name);
            ELSE
                EXECUTE format('DELETE FROM public.%I child WHERE child.%I IS NOT NULL AND NOT EXISTS (SELECT 1 FROM public.users parent WHERE parent.id = child.%I)', reference.table_name, reference.column_name, reference.column_name, reference.column_name);
            END IF;
        END IF;
    END LOOP;
END;
$$;
-- +goose StatementEnd

ALTER TABLE public.push_devices
    ADD CONSTRAINT push_devices_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE public.web_push_subscriptions
    ADD CONSTRAINT web_push_subscriptions_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE public.notification_webhooks
    ADD CONSTRAINT notification_webhooks_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE public.notification_email_prefs
    ADD CONSTRAINT notification_email_prefs_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE public.notification_discord_prefs
    ADD CONSTRAINT notification_discord_prefs_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE public.notification_discord_link_state
    ADD CONSTRAINT notification_discord_link_state_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE public.notification_deliveries
    ADD CONSTRAINT notification_deliveries_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE public.profile_series_interest
    ADD CONSTRAINT profile_series_interest_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE public.user_audio_preferences
    ADD CONSTRAINT user_audio_preferences_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE public.recommendation_cache
    ADD CONSTRAINT recommendation_cache_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE public.playback_sessions_sync
    ADD CONSTRAINT playback_sessions_sync_user_id_fkey FOREIGN KEY (user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE public.jellycompat_sessions
    ADD CONSTRAINT jellycompat_sessions_streamapp_user_id_fkey FOREIGN KEY (streamapp_user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE public.admin_jobs
    ADD CONSTRAINT admin_jobs_created_by_user_id_fkey FOREIGN KEY (created_by_user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE public.auth_sessions
    ADD CONSTRAINT auth_sessions_impersonator_user_id_fkey FOREIGN KEY (impersonator_user_id)
    REFERENCES public.users(id) ON DELETE CASCADE;

ALTER TABLE public.activity_log
    ADD CONSTRAINT activity_log_impersonator_user_id_fkey FOREIGN KEY (impersonator_user_id)
    REFERENCES public.users(id) ON DELETE SET NULL;

ALTER TABLE public.subtitle_ai_jobs
    ADD CONSTRAINT subtitle_ai_jobs_requested_by_fkey FOREIGN KEY (requested_by)
    REFERENCES public.users(id) ON DELETE SET NULL;

ALTER TABLE public.metadata_translation_jobs
    ADD CONSTRAINT metadata_translation_jobs_requested_by_fkey FOREIGN KEY (requested_by)
    REFERENCES public.users(id) ON DELETE SET NULL;

-- +goose Down
-- Restore enforcement only; neither direction edits existing values.
ALTER TABLE public.metadata_translation_jobs DROP CONSTRAINT metadata_translation_jobs_requested_by_fkey;
ALTER TABLE public.subtitle_ai_jobs DROP CONSTRAINT subtitle_ai_jobs_requested_by_fkey;
ALTER TABLE public.activity_log DROP CONSTRAINT activity_log_impersonator_user_id_fkey;
ALTER TABLE public.auth_sessions DROP CONSTRAINT auth_sessions_impersonator_user_id_fkey;
ALTER TABLE public.admin_jobs DROP CONSTRAINT admin_jobs_created_by_user_id_fkey;
ALTER TABLE public.jellycompat_sessions DROP CONSTRAINT jellycompat_sessions_streamapp_user_id_fkey;
ALTER TABLE public.playback_sessions_sync DROP CONSTRAINT playback_sessions_sync_user_id_fkey;
ALTER TABLE public.recommendation_cache DROP CONSTRAINT recommendation_cache_user_id_fkey;
ALTER TABLE public.user_audio_preferences DROP CONSTRAINT user_audio_preferences_user_id_fkey;
ALTER TABLE public.profile_series_interest DROP CONSTRAINT profile_series_interest_user_id_fkey;
ALTER TABLE public.notification_deliveries DROP CONSTRAINT notification_deliveries_user_id_fkey;
ALTER TABLE public.notification_discord_link_state DROP CONSTRAINT notification_discord_link_state_user_id_fkey;
ALTER TABLE public.notification_discord_prefs DROP CONSTRAINT notification_discord_prefs_user_id_fkey;
ALTER TABLE public.notification_email_prefs DROP CONSTRAINT notification_email_prefs_user_id_fkey;
ALTER TABLE public.notification_webhooks DROP CONSTRAINT notification_webhooks_user_id_fkey;
ALTER TABLE public.web_push_subscriptions DROP CONSTRAINT web_push_subscriptions_user_id_fkey;
ALTER TABLE public.push_devices DROP CONSTRAINT push_devices_user_id_fkey;
