-- +goose NO TRANSACTION
-- +goose Up
-- Give every foreign-key column a leading index.
--
-- Postgres indexes the referenced side of a foreign key (the parent's primary
-- or unique key) automatically and the referencing side not at all. Every
-- DELETE or key UPDATE on the parent therefore has to prove no child row
-- references the vanishing key, and without an index whose FIRST key column is
-- the referencing column that proof is a sequential scan of the whole child
-- table -- per deleted row, holding a row lock the entire time. Deleting one
-- user currently scans auth_sessions, playback_v3_attempts and
-- playback_route_events end to end; deleting one media file scans downloads and
-- abs_playback_sessions; removing a media item scans library_collection_items.
--
-- The audit that produced this list, run against a scratch database with the
-- full chain applied:
--
--   SELECT DISTINCT c.relname, a.attname
--   FROM pg_constraint con
--   JOIN pg_class c ON c.oid = con.conrelid
--   JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
--   JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = con.conkey[1]
--   WHERE con.contype = 'f' AND NOT c.relispartition
--     AND NOT EXISTS (
--       SELECT 1 FROM pg_index i
--       WHERE i.indrelid = con.conrelid AND i.indkey[0] = con.conkey[1]
--         AND i.indpred IS NULL
--     );
--
-- conkey[1], indkey[0] and indpred are the three points. A column that only
-- appears second in a composite index does not satisfy the reference check.
-- Neither does a column that leads only a PARTIAL index: the planner may use
-- such an index for the check only when the predicate provably holds for the
-- rows being proved absent, so the audit refuses to count partial indexes at
-- all (indpred IS NULL) and any exception is argued per case below rather than
-- assumed. A column that leads a total primary key, unique constraint, or plain
-- index already satisfies the check and is deliberately absent from the list.
--
-- 47 columns come from the constraints that already existed; the remaining 10
-- are columns that 20260901161700_user_fk_integrity constrains in this same
-- release and that had no leading index either. The other seven columns it
-- constrains already lead an index -- five a primary key or plain index, two a
-- partial one covered by the review below -- and are not duplicated here.
--
-- Two of the 47 lead a partial index and would have escaped a laxer audit that
-- counted any leading index as coverage. They are here because their predicate
-- turns on a column OTHER than the referencing one, so it excludes rows the
-- cascade must still visit:
--
--   * watch_provider_scrobble_sessions.connection_id -- its primary key is
--     (playback_session_id, connection_id), so connection_id is second there,
--     and idx_watch_provider_scrobble_sessions_open leads on connection_id but
--     is restricted to WHERE stop_sent_at IS NULL. The CASCADE from
--     watch_provider_connections has to visit ENDED sessions too, and those are
--     exactly the rows that index excludes. Nothing deletes them on a schedule,
--     so they accumulate for the life of the deployment and every provider
--     disconnect seq-scans the pile.
--   * page_sections.library_id -- idx_page_sections_library leads on library_id
--     but is restricted to WHERE enabled = true AND library_id IS NOT NULL, so
--     the CASCADE from media_folders cannot use it for disabled sections. The
--     table holds a few rows per library, so the plain index below costs almost
--     nothing; it is here to keep the rule uniform rather than to fix a hot
--     path.
--
-- Re-running the audit above after this migration still returns nine distinct
-- table/column pairs (plus one row per activity_log partition, which inherits
-- the parent's constraint and partial index and is covered by the same
-- argument; the partition count grows weekly), and all nine are the documented
-- exception rather than a miss. Every one of them
-- leads a partial index whose predicate is exactly "<that same column> IS NOT
-- NULL", which every cascade-relevant row satisfies by definition: a child row
-- matching a parent key cannot have a NULL in the referencing column. The index
-- therefore covers the whole row set the action visits.
--
--   activity_log.impersonator_user_id, auth_sessions.impersonator_user_id
--     (constrained by 20260901161700 in this release)
--   episode_libraries.first_seen_scan_run_id, history_import_runs.mapping_id,
--   marker_edit_audit.user_id, media_files.episode_id, media_files.extra_id,
--   media_files.first_seen_scan_run_id,
--   push_delivery_attempts.notification_delivery_id
--
-- Any future addition to that list needs the same argument written down; the
-- default answer for a partial index is that it does not count.
--
-- Every index is built CONCURRENTLY and this migration runs outside a
-- transaction: a plain CREATE INDEX takes a SHARE lock that blocks all writes
-- to the table for the length of the build, and this builds 57 of them. The
-- cost is that a CONCURRENTLY build which fails leaves an invalid index behind,
-- so the block below drops any invalid leftovers from an earlier attempt before
-- rebuilding -- the same recovery the existing concurrent-index migrations do,
-- expressed as one loop because of the number of indexes involved.
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
              'idx_policy_document_versions_created_by_user_id',
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

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_policy_document_versions_created_by_user_id
    ON public.policy_document_versions (created_by_user_id);

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
-- Drop the 57 indexes again. Nothing else in the schema depends on them: the
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
DROP INDEX CONCURRENTLY IF EXISTS public.idx_policy_document_versions_created_by_user_id;
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
