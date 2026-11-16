-- +goose Up
-- +goose StatementBegin

-- Personal ratings move from whole stars (1 to 5) to scores (1 to 10), shown
-- as five stars with half stars. Every stored rating doubles, so 4 stars
-- becomes 8 and means the same as before.
ALTER TABLE public.user_ratings DROP CONSTRAINT user_ratings_rating_check;
UPDATE public.user_ratings SET rating = rating * 2;
ALTER TABLE public.user_ratings
    ADD CONSTRAINT user_ratings_rating_check CHECK (rating BETWEEN 1 AND 10);

-- The rating each watch provider connection last agreed on doubles with it,
-- so the next sync sees no change on Silo's side. A provider value that was
-- rounded into a star (a 7 kept as 4 stars, now 8) then differs from the
-- agreed value on the provider's side only, and the sync imports it exactly.
ALTER TABLE public.watch_provider_rating_items
    DROP CONSTRAINT watch_provider_rating_items_synced_rating_check;
UPDATE public.watch_provider_rating_items SET synced_rating = synced_rating * 2;
ALTER TABLE public.watch_provider_rating_items
    ADD CONSTRAINT watch_provider_rating_items_synced_rating_check CHECK (synced_rating BETWEEN 1 AND 10);

-- An incremental read only returns ratings that changed since its cursor, so
-- a provider would never resend an unchanged 7. Dropping the rating read
-- cursors (every key containing ".ratings", as withoutRatingCursors in
-- internal/watchsync does) makes the next sync read every provider rating.
UPDATE public.watch_provider_connections
SET sync_cursors = (
    SELECT COALESCE(jsonb_object_agg(key, value), '{}'::jsonb)
    FROM jsonb_each(sync_cursors)
    WHERE strpos(key, '.ratings') = 0
)
WHERE EXISTS (SELECT 1 FROM jsonb_object_keys(sync_cursors) AS k WHERE strpos(k, '.ratings') > 0);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Back to whole stars, rounding half up. Half-star scores lose their half.
ALTER TABLE public.user_ratings DROP CONSTRAINT user_ratings_rating_check;
UPDATE public.user_ratings SET rating = (rating + 1) / 2;
ALTER TABLE public.user_ratings
    ADD CONSTRAINT user_ratings_rating_check CHECK (rating >= 1 AND rating <= 5);

ALTER TABLE public.watch_provider_rating_items
    DROP CONSTRAINT watch_provider_rating_items_synced_rating_check;
UPDATE public.watch_provider_rating_items SET synced_rating = (synced_rating + 1) / 2;
ALTER TABLE public.watch_provider_rating_items
    ADD CONSTRAINT watch_provider_rating_items_synced_rating_check CHECK (synced_rating BETWEEN 1 AND 5);
-- +goose StatementEnd
