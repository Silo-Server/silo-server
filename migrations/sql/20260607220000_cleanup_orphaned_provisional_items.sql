-- +goose Up
-- +goose StatementBegin
-- Clean up provisional catalog rows left behind by library-delete/matcher races.
-- Stale external IDs and other item-owned metadata cascade from media_items.
-- Durable user/sync references do not all have media_items foreign keys, so
-- keep any provisional row that is still referenced outside derived metadata.
DELETE FROM public.media_items mi
WHERE mi.status IN ('pending', 'unmatched', 'ambiguous')
  AND NOT EXISTS (
    SELECT 1 FROM public.media_item_libraries mil
    WHERE mil.content_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.media_files mf
    WHERE mf.content_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.episodes e
    WHERE e.series_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.seasons s
    WHERE s.series_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.library_collection_items lci
    WHERE lci.media_item_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.abs_bookmarks ab
    WHERE ab.library_item_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.abs_playback_sessions aps
    WHERE aps.content_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.abs_rss_feeds arf
    WHERE arf.library_item_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.downloads d
    WHERE d.content_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.playback_history_admin pha
    WHERE pha.media_item_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.plex_sync_item_bindings psib
    WHERE psib.media_item_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.plex_sync_item_state psis
    WHERE psis.media_item_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.podcast_feeds pf
    WHERE pf.media_item_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.user_favorites uf
    WHERE uf.media_item_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.user_downloads ud
    WHERE ud.media_item_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.user_history_hidden_items uhhi
    WHERE uhhi.media_item_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.user_home_item_dismissals uhid
    WHERE uhid.media_item_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.user_home_item_dismissals uhid_series
    WHERE uhid_series.series_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.user_audio_preferences uap
    WHERE uap.series_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.user_personal_collection_items upci
    WHERE upci.media_item_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.user_ratings ur
    WHERE ur.media_item_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.user_series_playback_preferences uspp
    WHERE uspp.series_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.user_subtitle_preferences usp
    WHERE usp.series_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.user_watch_history uwh
    WHERE uwh.media_item_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.user_watch_progress uwp
    WHERE uwp.media_item_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.user_watchlist uwl
    WHERE uwl.media_item_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.watch_provider_favorite_items wpfi
    WHERE wpfi.media_item_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.watch_provider_history_exports wphe
    WHERE wphe.media_item_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.watch_provider_scrobble_sessions wpss
    WHERE wpss.media_item_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.watch_together_rooms wtr
    WHERE wtr.selected_content_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.watch_together_suggestions wts
    WHERE wts.content_id = mi.content_id
  )
  AND NOT EXISTS (
    SELECT 1 FROM public.webhook_sync_item_state wsis
    WHERE wsis.media_item_id = mi.content_id
  );
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Data cleanup only; deleted orphaned provisional rows cannot be reconstructed.
-- +goose StatementEnd
