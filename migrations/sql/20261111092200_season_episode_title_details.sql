-- +goose Up
-- Title details an admin edits on a season or episode. No metadata provider
-- writes them. Episodes already store their provider IDs; seasons now do too,
-- without the unique indexes episodes have, since nothing looks a season up by
-- them. A season or episode keeps the series' content rating, which decides
-- age limits.
ALTER TABLE public.seasons
    ADD COLUMN sort_title text DEFAULT ''::text NOT NULL,
    ADD COLUMN original_title text DEFAULT ''::text NOT NULL,
    ADD COLUMN tagline text DEFAULT ''::text NOT NULL,
    ADD COLUMN imdb_id text DEFAULT ''::text NOT NULL,
    ADD COLUMN tmdb_id text DEFAULT ''::text NOT NULL,
    ADD COLUMN tvdb_id text DEFAULT ''::text NOT NULL;
ALTER TABLE public.episodes
    ADD COLUMN sort_title text DEFAULT ''::text NOT NULL,
    ADD COLUMN original_title text DEFAULT ''::text NOT NULL,
    ADD COLUMN tagline text DEFAULT ''::text NOT NULL;

-- +goose Down
ALTER TABLE public.episodes
    DROP COLUMN tagline,
    DROP COLUMN original_title,
    DROP COLUMN sort_title;
ALTER TABLE public.seasons
    DROP COLUMN tvdb_id,
    DROP COLUMN tmdb_id,
    DROP COLUMN imdb_id,
    DROP COLUMN tagline,
    DROP COLUMN original_title,
    DROP COLUMN sort_title;
