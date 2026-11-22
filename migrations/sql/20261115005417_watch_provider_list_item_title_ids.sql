-- +goose Up
-- A watchlist entry outside the library has a shadow row named for one TMDB
-- ID. When promotion moves the entry onto the library watchlist, the title and
-- its other IDs are deleted, so the row keeps every ID the title has held
-- ("tmdb:550", "imdb:tt0137523", "tvdb:121361") to find the library item
-- afterwards. A library item that took over a title row at promotion keeps
-- them too; other library item rows leave it empty. A constant default keeps this
-- a metadata-only change; the next migration fills existing rows.
ALTER TABLE public.watch_provider_list_items
    ADD COLUMN IF NOT EXISTS title_ids text[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE public.watch_provider_list_items
    DROP COLUMN IF EXISTS title_ids;
