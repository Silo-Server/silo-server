-- +goose NO TRANSACTION

-- +goose Up
-- Equal normalized episode titles share every relevance score when no year or
-- phrase hint is present. This index supplies the remaining title/ID order in
-- one library, allowing a full exact tier to satisfy a page before lower tiers
-- are scored. Retain the other search indexes for all general requests.
--
-- Hash the equality key rather than repeating normalized/raw title text: two
-- title copies can exceed PostgreSQL's B-tree tuple limit for accepted metadata.
-- The maintained sort_key is LOWER(title), where title is the BTRIM/fallback
-- projection. Replacing the four-byte year in the predecessor folder/year/
-- sort_key/episode_id index with a four-byte hash keeps the same tuple-width
-- shape. Queries recheck normalized-title equality before LIMIT for collisions.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON n.oid=c.relnamespace
        JOIN pg_index i ON i.indexrelid=c.oid
        WHERE n.nspname='public' AND c.relname='idx_episode_catalog_entries_exact_search_page'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.idx_episode_catalog_entries_exact_search_page;
    END IF;
END;
$$;
-- +goose StatementEnd
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_episode_catalog_entries_exact_search_page
    ON public.episode_catalog_entries (media_folder_id, hashtext(search_title_normalized), LOWER(title), episode_id);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_episode_catalog_entries_exact_search_page;
