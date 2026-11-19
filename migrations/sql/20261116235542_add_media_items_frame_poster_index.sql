-- +goose NO TRANSACTION
-- +goose Up
-- The frame poster pass first clears the frame posters of movies whose files
-- are now in more than one library
-- (ItemRepository.ClearFramePostersSpanningLibraries). A frame poster's source
-- starts with "frame:" (catalog.StillSourceFramePrefix), and no index serves a
-- prefix test on poster_source_path, so without this index every run of the
-- pass, every 15 minutes, reads all of media_items to find the few frame
-- posters. The predicate must stay the exact expression the cleanup uses,
-- poster_source_path LIKE 'frame:%', so the planner can prove the match.
--
-- CONCURRENTLY so this does not take a write lock on a running server, which
-- requires NO TRANSACTION. A failed nontransactional run can leave an invalid
-- index behind, which IF NOT EXISTS would keep, so the index is dropped
-- concurrently first and a retry rebuilds it, as in
-- 20260914145000_index_artwork_reference_paths.sql.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_frame_poster;
CREATE INDEX CONCURRENTLY idx_media_items_frame_poster ON public.media_items (content_id) WHERE poster_source_path LIKE 'frame:%';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_frame_poster;
