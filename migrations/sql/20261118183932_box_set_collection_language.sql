-- +goose Up
-- The metadata language a TMDB collection's name, overview and artwork were
-- fetched in. The box set sync fetches each collection in the language of its
-- first box set library and fetches it again when that language changes.
-- Collections stored before this column were fetched without a language,
-- which TMDB answers in English.
ALTER TABLE public.tmdb_movie_collections
    ADD COLUMN language text NOT NULL DEFAULT 'en';

-- +goose Down
ALTER TABLE public.tmdb_movie_collections DROP COLUMN IF EXISTS language;
