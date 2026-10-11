-- +goose NO TRANSACTION

-- +goose Up
-- Discovery rows (Critically Acclaimed, Hidden Gems, Forgotten Favorites,
-- Short & Sweet, Genre Roulette, moods) rank titles by TMDB vote average pulled
-- toward 6.5 by their vote count: (votes × average + 500 × 6.5) /
-- (votes + 500). This indexes that exact expression, which
-- catalog.TMDBWeightedRatingSQL must keep matching, so a row walks the
-- best-ranked titles instead of sorting every candidate.
--
-- The migration only runs when it is not yet recorded, so any index found here
-- is a leftover of an interrupted build. Drop it concurrently so a retry never
-- takes an exclusive lock on media_items.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_tmdb_weighted_rating;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_items_tmdb_weighted_rating
    ON public.media_items (((tmdb_vote_count * tmdb_vote_average + 3250) / (tmdb_vote_count + 500)) DESC NULLS LAST, content_id)
    WHERE tmdb_vote_count IS NOT NULL AND tmdb_vote_average IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_tmdb_weighted_rating;
