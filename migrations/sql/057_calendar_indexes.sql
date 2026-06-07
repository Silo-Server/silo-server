-- +goose Up
-- +goose StatementBegin
-- Calendar feature: add indexes on date columns for efficient range queries.
CREATE INDEX IF NOT EXISTS idx_episodes_air_date
    ON episodes (air_date)
    WHERE air_date IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_seasons_air_date
    ON seasons (air_date)
    WHERE air_date IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_media_items_movie_release_date
    ON media_items (release_date, content_id)
    WHERE type = 'movie' AND release_date IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_episodes_air_date;
DROP INDEX IF EXISTS idx_seasons_air_date;
DROP INDEX IF EXISTS idx_media_items_movie_release_date;
-- +goose StatementEnd
