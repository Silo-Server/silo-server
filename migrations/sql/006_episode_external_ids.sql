-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.episodes
    ADD COLUMN IF NOT EXISTS imdb_id text DEFAULT ''::text NOT NULL,
    ADD COLUMN IF NOT EXISTS tmdb_id text DEFAULT ''::text NOT NULL,
    ADD COLUMN IF NOT EXISTS tvdb_id text DEFAULT ''::text NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_episodes_imdb_id
    ON public.episodes USING btree (imdb_id)
    WHERE (imdb_id <> ''::text);

CREATE UNIQUE INDEX IF NOT EXISTS idx_episodes_tmdb_id
    ON public.episodes USING btree (tmdb_id)
    WHERE (tmdb_id <> ''::text);

CREATE UNIQUE INDEX IF NOT EXISTS idx_episodes_tvdb_id
    ON public.episodes USING btree (tvdb_id)
    WHERE (tvdb_id <> ''::text);
-- +goose StatementEnd
