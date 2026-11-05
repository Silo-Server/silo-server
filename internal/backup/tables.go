package backup

import "strings"

// Every table of the schema is classified once, so a new one cannot reach a
// backup or a restore without someone deciding how:
//
//   - secretTables: tables holding a bearer secret, a capability, or a link
//     that grants access or reaches a device, each left out of the dump,
//     carried from the live server on a full restore, or restored with a
//     reason;
//   - liveTables: tables a restore takes from the live server by a step
//     of their own;
//   - loggedTables: logs and queues the dump leaves out;
//   - restoredDataTables: the server's data, restored as the backup has it;
//   - serverStateTables: state of the running server (caches, queues, job,
//     lease, and lock state, revisions, samples, receipts), restored as the
//     backup has it and rebuilt or reconciled by the server;
//   - stricterFlags (credentials_stricter.go): security switches inside a
//     table restored as the backup has it, which a full restore keeps at
//     the stricter of the backup's and the live server's value.
//
// TestEveryTableIsClassifiedDB reads every table after the migrations and
// fails on one in none of the lists, in more than one, or on a listed name
// that is not a table. A layer that adds a table classifies it here.

// liveTables are restored from the live server by a step of their own
// rather than as the backup has them: the custom access policies
// (CarryPolicies), whose documents and versions reference each other and
// carry as one unit.
var liveTables = []string{"policy_documents", "policy_document_versions"}

// loggedTables are the logs and queues whose rows the dump leaves out:
// large, rebuildable, or disposable. media_intro_fingerprints is left out
// unless backup.include_intro_fingerprints (excludedTableData).
var loggedTables = []string{"activity_log", "operational_logs", "policy_decisions", "catalog_search_index_events", tableIntroFingerprints}

const tableIntroFingerprints = "media_intro_fingerprints"

// restoredDataTables are the server's data: the library, catalog,
// metadata, accounts and profiles with their data, and configuration.
var restoredDataTables = strings.Fields(`
	abs_bookmarks access_groups admin_dashboard_layouts admin_playback_history anime_ids
	audiobook_series autoscan_settings autoscan_sources client_diagnostic_reports download_artifacts
	download_subscriptions downloaded_subtitles downloads ebook_reader_annotations ebook_reader_config
	ebook_reader_progress ebook_series embedded_subtitle_tracks episode_availability
	episode_catalog_entries episode_libraries episode_localizations episodes external_subtitle_timings
	history_import_user_mappings item_availability item_people item_theme_songs item_videos
	jellycompat_displayprefs library_collection_items library_collection_libraries
	library_provider_chains literary_work_items literary_work_match_decisions literary_works
	local_metadata_default_on_chain_backup manga_chapters marker_provider_config media_extras
	media_file_trickplay media_files media_folder_paths media_folders media_group_locations
	media_group_overrides media_identity_overrides media_item_aliases media_item_backdrops
	media_item_groups media_item_libraries media_item_localizations media_item_provider_ids
	media_item_rating_sources media_item_roots media_item_tmdb_collections media_items
	media_request_events media_request_follows media_request_targets media_requests
	media_root_overrides movie_availability notification_preferences observed_media_locations
	overlay_images page_sections people plugin_archives plugin_capabilities plugin_installations 
	 plugin_task_bindings podcast_feeds 
	 profile_series_interest profile_series_notification_mutes request_group_limits
	request_routes request_routing request_settings request_user_limits scanned_media_groups
	scanned_media_roots season_localizations seasons server_settings skipped_media_roots
	stale_media_ids task_schedules task_triggers tmdb_movie_collections user_audio_preferences
	user_collection_sort_preferences user_device_settings user_devices user_downloads
	user_dropped_series user_favorites user_history_hidden_items user_home_item_dismissals
	user_library_playback_preferences user_login_identifiers user_not_interested
	user_personal_collection_items user_personal_collection_profiles user_personal_collections
	user_profile_allowed_libraries user_profile_onboarding user_ratings
	user_series_playback_preferences user_setting_values user_settings user_subtitle_preferences
	user_watch_history user_watch_progress user_watchlist user_watchlist_titles
	watchlist_title_aliases watchlist_titles`)

// serverStateTables describe the running server rather than its data.
var serverStateTables = strings.Fields(`
	admin_dashboard_layout_revisions admin_jobs anime_ids_refresh
	api_client_usage_hourly artwork_revision_gc_candidates artwork_revision_gc_dormant_cursor
	audiobook_item_file_stats autoscan_events autoscan_webhook_deliveries blob_gc_queue
	catalog_item_change_drain catalog_item_change_events catalog_search_index_state
	dashboard_metric_samples dashboard_node_egress_samples download_artifact_orphans
	download_storage_events download_storage_samples ebook_enrichment_reconcile_cursors
	goose_db_version history_import_runs image_ladder_backfill_state intro_season_analysis_state
	intro_silence_refinement_attempts item_cowatch library_collection_order_revisions
	library_collection_poster_variants library_collection_revisions library_collection_sync_runs
	library_monitor_status local_sidecar_fingerprints manga_enrichment_state marker_provider_cooldowns
	matroska_track_backfill_checks media_item_alias_backfill_state media_item_embeddings
	metadata_enrichment_state metadata_image_cache_jobs metadata_refresh_debt
	metadata_translation_jobs node_heartbeats notification_backfill_state
	notification_content_seed_state notification_deliveries notification_inbox_clocks
	notification_library_seed_state page_section_revisions page_section_scope_revisions
	playback_connections playback_route_events playback_session_lifecycle playback_sessions_sync
	playback_shuffle_items playback_shuffles playback_v3_attempts playback_v3_replans
	plugin_event_deliveries  policy_generation progress_bootstrap_items
	progress_bootstrap_snapshots push_delivery_attempts recommendation_cache release_events
	request_discover_layout request_discover_list_snapshots scan_runs seasonal_list_snapshots
	series_match_queue storage_transition_checkpoints storage_transition_cursors
	stream_node_pool_generation  subtitle_ai_jobs subtitle_sync_jobs task_executions
	trending_discover_snapshots user_aggregates user_collection_order_revisions
	user_collection_revisions user_last_activity user_personal_collection_poster_variants
	user_personal_collection_smart_collages user_progress_sync_state user_setting_migration_rejects
	user_setting_mutations user_taste_clusters user_taste_profiles userdb_import_receipts
	watch_provider_dropped_items watch_provider_history_exports watch_provider_list_items
	watch_provider_rating_items watch_provider_scrobble_sessions watch_provider_sync_runs
	watch_together_room_creation_receipts watch_together_suggestion_receipts webhook_sync_event_logs
	webhook_sync_item_state`)
