-- +goose NO TRANSACTION

-- +goose Up
-- Discover's live default rows, Recently Added and Highly Rated in Your
-- Library, read each recommendable media type newest or best rated first.
-- These indexes supply that order per type, so a read walks only until the
-- row is full instead of sorting every movie and series in the catalog. The
-- expressions repeat addedAtSQL and catalogRatingOrderSQL in
-- internal/recommendations/repo.go and must stay identical to them.
-- An earlier interrupted build can leave an invalid index behind; drop it
-- so IF NOT EXISTS does not keep it.
-- +goose StatementBegin
DO $$
DECLARE
    name text;
BEGIN
    FOREACH name IN ARRAY ARRAY['idx_media_items_type_added_at', 'idx_media_items_type_catalog_rating'] LOOP
        IF EXISTS (
            SELECT 1 FROM pg_class c
            JOIN pg_namespace n ON n.oid = c.relnamespace
            JOIN pg_index i ON i.indexrelid = c.oid
            WHERE n.nspname = 'public' AND c.relname = name AND NOT i.indisvalid
        ) THEN
            EXECUTE format('DROP INDEX public.%I', name);
        END IF;
    END LOOP;
END;
$$;
-- +goose StatementEnd
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_items_type_added_at
    ON public.media_items (type, GREATEST(created_at, COALESCE(latest_episode_added_at, created_at)) DESC, content_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_items_type_catalog_rating
    ON public.media_items (
        type,
        (CASE WHEN rating_imdb IS NOT NULL THEN 2 WHEN rating_tmdb IS NOT NULL AND rating_tmdb < 9.5 THEN 1 ELSE 0 END) DESC,
        rating_imdb DESC NULLS LAST,
        (CASE WHEN rating_tmdb < 9.5 THEN rating_tmdb END) DESC NULLS LAST,
        content_id
    );

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_type_catalog_rating;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_type_added_at;
