-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.watch_provider_scrobble_sessions
    ADD COLUMN IF NOT EXISTS provider_item_key text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS imdb_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS tmdb_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS tvdb_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS series_imdb_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS series_tmdb_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS series_tvdb_id text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS season_number integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS episode_number integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS duration_seconds double precision NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS completed boolean NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.watch_provider_scrobble_sessions
    DROP COLUMN IF EXISTS completed,
    DROP COLUMN IF EXISTS duration_seconds,
    DROP COLUMN IF EXISTS episode_number,
    DROP COLUMN IF EXISTS season_number,
    DROP COLUMN IF EXISTS series_tvdb_id,
    DROP COLUMN IF EXISTS series_tmdb_id,
    DROP COLUMN IF EXISTS series_imdb_id,
    DROP COLUMN IF EXISTS tvdb_id,
    DROP COLUMN IF EXISTS tmdb_id,
    DROP COLUMN IF EXISTS imdb_id,
    DROP COLUMN IF EXISTS kind,
    DROP COLUMN IF EXISTS provider_item_key;
-- +goose StatementEnd
