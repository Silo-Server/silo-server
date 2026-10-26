-- +goose NO TRANSACTION

-- +goose Up
-- Every index dropped here is non-unique and backs no constraint, and the
-- planner gets the same access path from another index, so each one only costs
-- writes. Most sit on tables that library scans, metadata refreshes and
-- progress reports rewrite constantly.

-- Each one's columns, opclasses, order and predicate are a leading prefix of
-- an index the table keeps (named after the arrow). idx_user_watch_progress_profile
-- and idx_media_files_folder stay although their supersets could serve them:
-- deduplication keeps them several times smaller, so reading a whole profile's
-- progress or counting a folder's files touches far fewer pages.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_uwp_profile_completed; -- idx_uwp_profile_completed_cursor
DROP INDEX CONCURRENTLY IF EXISTS public.idx_item_libraries_content; -- media_item_libraries_pkey
DROP INDEX CONCURRENTLY IF EXISTS public.idx_item_libraries_folder; -- idx_item_libraries_folder_seen_content
DROP INDEX CONCURRENTLY IF EXISTS public.idx_item_people_content_id; -- idx_item_people_content_kind_person
DROP INDEX CONCURRENTLY IF EXISTS public.idx_item_people_person_id; -- idx_item_people_person_kind
-- Scanned backward, the descending index returns the ascending order.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_episodes_air_date; -- idx_episodes_air_date_content

-- Exact copies of a primary key or unique constraint.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_series_match_queue_folder;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_series_root_match_queue_folder;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_literary_work_items_content;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_watch_provider_connections_provider_enabled;

-- No query reads these. Episode search matches the stored vectors on
-- episode_catalog_entries, and taste clusters are only read by profile, never
-- by distance, yet every cluster rewrite pays for the HNSW graph.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_episodes_search_title;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_episodes_search_overview;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_taste_clusters_embedding;

-- +goose Down
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_uwp_profile_completed
    ON public.user_watch_progress USING btree (user_id, profile_id, updated_at DESC)
    WHERE completed = TRUE;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_item_libraries_content
    ON public.media_item_libraries USING btree (content_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_item_libraries_folder
    ON public.media_item_libraries USING btree (media_folder_id, first_seen_at DESC);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_item_people_content_id
    ON public.item_people USING btree (content_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_item_people_person_id
    ON public.item_people USING btree (person_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_episodes_air_date
    ON public.episodes USING btree (air_date)
    WHERE air_date IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_series_match_queue_folder
    ON public.series_match_queue USING btree (media_folder_id, group_key_version, content_group_key);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_series_root_match_queue_folder
    ON public.series_root_match_queue USING btree (media_folder_id, observed_root_path);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_literary_work_items_content
    ON public.literary_work_items USING btree (content_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_watch_provider_connections_provider_enabled
    ON public.watch_provider_connections USING btree (provider, user_id, profile_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_episodes_search_title
ON public.episodes USING gin (
    setweight(
        to_tsvector(
            'simple',
            public.normalize_search_text(
                COALESCE(NULLIF(BTRIM(title), ''), 'Episode ' || episode_number::text)
            )
        ),
        'A'
    )
);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_episodes_search_overview
ON public.episodes USING gin (
    to_tsvector('english', COALESCE(overview, ''))
);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_taste_clusters_embedding
    ON public.user_taste_clusters USING hnsw ((embedding::halfvec(3072)) halfvec_cosine_ops);
