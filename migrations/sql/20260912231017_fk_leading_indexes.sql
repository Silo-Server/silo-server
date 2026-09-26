-- +goose NO TRANSACTION
-- +goose Up
-- Schema hygiene stage 2; follows the apiv2 replacement of PR #879.
-- Build missing child-side FK indexes before validating the user constraints.
-- A leading partial index counts only when its predicate is exactly the same
-- column IS NOT NULL. In particular, the auth_sessions revoked_at, subtitle job
-- kind, page_sections enabled, and scrobble stop_sent_at predicates omit rows
-- that parent deletion must visit. policy_versions_author_document_idx already
-- covers created_by_user_id and is deliberately not duplicated here.
--
-- Concurrent builds avoid blocking writers. An interrupted build can leave an
-- invalid index; remove only this migration's invalid leftovers before retry.
-- Recovery uses ordinary DROP INDEX inside the DO transaction, so it can block
-- writers briefly. Successful builds survive a later failure and are reused.
-- +goose StatementBegin
DO $$
DECLARE
    leftover text;
BEGIN
    FOR leftover IN
        SELECT c.relname
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND NOT i.indisvalid
          AND c.relname = ANY (ARRAY[
              'idx_abs_playback_sessions_content_id',
              'idx_abs_playback_sessions_media_file_id',
              'idx_abs_rss_feeds_library_item_id',
              'idx_admin_jobs_created_by_user_id',
              'idx_auth_sessions_user_id',
              'idx_autoscan_connections_request_integration_id',
              'idx_autoscan_sources_connection_id',
              'idx_autoscan_webhook_deliveries_source_id',
              'idx_device_login_requests_approved_by_user_id',
              'idx_device_login_requests_auth_session_id',
              'idx_downloaded_subtitles_downloaded_by',
              'idx_downloads_media_file_id',
              'idx_intro_season_analysis_state_media_folder_id',
              'idx_invitations_accepted_user_id',
              'idx_invitations_access_group_id',
              'idx_invitations_invited_by',
              'idx_invite_codes_created_by',
              'idx_jellycompat_sessions_streamapp_user_id',
              'idx_library_collection_items_media_item_id',
              'idx_library_collection_libraries_group_id',
              'idx_library_provider_chains_plugin_installation_id',
              'idx_literary_work_match_decisions_created_by',
              'idx_literary_works_primary_cover_content_id',
              'idx_marker_edit_audit_api_key_id',
              'idx_marker_edit_audit_impersonator_user_id',
              'idx_media_group_overrides_created_by_user_id',
              'idx_media_group_overrides_updated_by_user_id',
              'idx_media_identity_overrides_created_by_user_id',
              'idx_media_identity_overrides_updated_by_user_id',
              'idx_media_request_events_actor_user_id',
              'idx_media_request_targets_integration_id',
              'idx_media_root_overrides_created_by_user_id',
              'idx_media_root_overrides_updated_by_user_id',
              'idx_metadata_translation_jobs_requested_by',
              'idx_notification_deliveries_release_event_id',
              'idx_notification_discord_link_state_user_id',
              'idx_notification_webhooks_user_id',
              'idx_page_sections_library_id',
              'idx_playback_route_events_user_id',
              'idx_playback_sessions_sync_user_id',
              'idx_playback_v3_attempts_effective_media_file_id',
              'idx_playback_v3_attempts_requested_media_file_id',
              'idx_playback_v3_attempts_user_id',
              'idx_plugin_auth_identities_user_id',
              'idx_plugin_installations_repository_id',
              'idx_policy_documents_active_version_id',
              'idx_profile_series_interest_user_id',
              'idx_push_devices_user_id',
              'idx_subtitle_ai_jobs_requested_by',
              'idx_user_profile_allowed_libraries_library_id',
              'idx_watch_provider_auth_sessions_user_id',
              'idx_watch_provider_connections_user_id',
              'idx_watch_provider_scrobble_sessions_connection_id',
              'idx_watch_together_rooms_host_user_id',
              'idx_watch_together_suggestions_suggester_user_id',
              'idx_web_push_subscriptions_user_id'
          ])
    LOOP
        EXECUTE format('DROP INDEX public.%I', leftover);
    END LOOP;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_abs_playback_sessions_content_id
    ON public.abs_playback_sessions (content_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_abs_playback_sessions_media_file_id
    ON public.abs_playback_sessions (media_file_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_abs_rss_feeds_library_item_id
    ON public.abs_rss_feeds (library_item_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_admin_jobs_created_by_user_id
    ON public.admin_jobs (created_by_user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_auth_sessions_user_id
    ON public.auth_sessions (user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_autoscan_connections_request_integration_id
    ON public.autoscan_connections (request_integration_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_autoscan_sources_connection_id
    ON public.autoscan_sources (connection_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_autoscan_webhook_deliveries_source_id
    ON public.autoscan_webhook_deliveries (source_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_device_login_requests_approved_by_user_id
    ON public.device_login_requests (approved_by_user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_device_login_requests_auth_session_id
    ON public.device_login_requests (auth_session_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_downloaded_subtitles_downloaded_by
    ON public.downloaded_subtitles (downloaded_by);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_downloads_media_file_id
    ON public.downloads (media_file_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_intro_season_analysis_state_media_folder_id
    ON public.intro_season_analysis_state (media_folder_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_invitations_accepted_user_id
    ON public.invitations (accepted_user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_invitations_access_group_id
    ON public.invitations (access_group_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_invitations_invited_by
    ON public.invitations (invited_by);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_invite_codes_created_by
    ON public.invite_codes (created_by);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_jellycompat_sessions_streamapp_user_id
    ON public.jellycompat_sessions (streamapp_user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_library_collection_items_media_item_id
    ON public.library_collection_items (media_item_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_library_collection_libraries_group_id
    ON public.library_collection_libraries (group_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_library_provider_chains_plugin_installation_id
    ON public.library_provider_chains (plugin_installation_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_literary_work_match_decisions_created_by
    ON public.literary_work_match_decisions (created_by);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_literary_works_primary_cover_content_id
    ON public.literary_works (primary_cover_content_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_marker_edit_audit_api_key_id
    ON public.marker_edit_audit (api_key_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_marker_edit_audit_impersonator_user_id
    ON public.marker_edit_audit (impersonator_user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_group_overrides_created_by_user_id
    ON public.media_group_overrides (created_by_user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_group_overrides_updated_by_user_id
    ON public.media_group_overrides (updated_by_user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_identity_overrides_created_by_user_id
    ON public.media_identity_overrides (created_by_user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_identity_overrides_updated_by_user_id
    ON public.media_identity_overrides (updated_by_user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_request_events_actor_user_id
    ON public.media_request_events (actor_user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_request_targets_integration_id
    ON public.media_request_targets (integration_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_root_overrides_created_by_user_id
    ON public.media_root_overrides (created_by_user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_root_overrides_updated_by_user_id
    ON public.media_root_overrides (updated_by_user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_metadata_translation_jobs_requested_by
    ON public.metadata_translation_jobs (requested_by);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_notification_deliveries_release_event_id
    ON public.notification_deliveries (release_event_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_notification_discord_link_state_user_id
    ON public.notification_discord_link_state (user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_notification_webhooks_user_id
    ON public.notification_webhooks (user_id);

-- page_sections.library_id: idx_page_sections_library leads on this column but
-- is partial (WHERE enabled = true AND library_id IS NOT NULL), so it cannot
-- serve the CASCADE from media_folders for disabled sections.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_page_sections_library_id
    ON public.page_sections (library_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_playback_route_events_user_id
    ON public.playback_route_events (user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_playback_sessions_sync_user_id
    ON public.playback_sessions_sync (user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_playback_v3_attempts_effective_media_file_id
    ON public.playback_v3_attempts (effective_media_file_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_playback_v3_attempts_requested_media_file_id
    ON public.playback_v3_attempts (requested_media_file_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_playback_v3_attempts_user_id
    ON public.playback_v3_attempts (user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_plugin_auth_identities_user_id
    ON public.plugin_auth_identities (user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_plugin_installations_repository_id
    ON public.plugin_installations (repository_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_policy_documents_active_version_id
    ON public.policy_documents (active_version_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_profile_series_interest_user_id
    ON public.profile_series_interest (user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_push_devices_user_id
    ON public.push_devices (user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_subtitle_ai_jobs_requested_by
    ON public.subtitle_ai_jobs (requested_by);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_user_profile_allowed_libraries_library_id
    ON public.user_profile_allowed_libraries (library_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_watch_provider_auth_sessions_user_id
    ON public.watch_provider_auth_sessions (user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_watch_provider_connections_user_id
    ON public.watch_provider_connections (user_id);

-- watch_provider_scrobble_sessions.connection_id: second in the primary key,
-- and the only index leading on it is partial (WHERE stop_sent_at IS NULL).
-- Ended sessions are never swept, so the CASCADE from watch_provider_connections
-- seq-scans an ever-growing table on every provider disconnect.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_watch_provider_scrobble_sessions_connection_id
    ON public.watch_provider_scrobble_sessions (connection_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_watch_together_rooms_host_user_id
    ON public.watch_together_rooms (host_user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_watch_together_suggestions_suggester_user_id
    ON public.watch_together_suggestions (suggester_user_id);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_web_push_subscriptions_user_id
    ON public.web_push_subscriptions (user_id);

-- +goose Down
-- Drop the 56 indexes again. Nothing else in the schema depends on them: the
-- foreign keys they serve remain valid, they just go back to scanning.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_web_push_subscriptions_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_watch_together_suggestions_suggester_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_watch_together_rooms_host_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_watch_provider_scrobble_sessions_connection_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_watch_provider_connections_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_watch_provider_auth_sessions_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_user_profile_allowed_libraries_library_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_subtitle_ai_jobs_requested_by;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_push_devices_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_profile_series_interest_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_policy_documents_active_version_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_plugin_installations_repository_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_plugin_auth_identities_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_playback_v3_attempts_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_playback_v3_attempts_requested_media_file_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_playback_v3_attempts_effective_media_file_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_playback_sessions_sync_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_playback_route_events_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_page_sections_library_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_notification_webhooks_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_notification_discord_link_state_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_notification_deliveries_release_event_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_metadata_translation_jobs_requested_by;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_root_overrides_updated_by_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_root_overrides_created_by_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_request_targets_integration_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_request_events_actor_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_identity_overrides_updated_by_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_identity_overrides_created_by_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_group_overrides_updated_by_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_group_overrides_created_by_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_marker_edit_audit_impersonator_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_marker_edit_audit_api_key_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_literary_works_primary_cover_content_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_literary_work_match_decisions_created_by;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_library_provider_chains_plugin_installation_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_library_collection_libraries_group_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_library_collection_items_media_item_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_jellycompat_sessions_streamapp_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_invite_codes_created_by;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_invitations_invited_by;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_invitations_access_group_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_invitations_accepted_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_intro_season_analysis_state_media_folder_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_downloads_media_file_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_downloaded_subtitles_downloaded_by;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_device_login_requests_auth_session_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_device_login_requests_approved_by_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_autoscan_webhook_deliveries_source_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_autoscan_sources_connection_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_autoscan_connections_request_integration_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_auth_sessions_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_admin_jobs_created_by_user_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_abs_rss_feeds_library_item_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_abs_playback_sessions_media_file_id;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_abs_playback_sessions_content_id;
