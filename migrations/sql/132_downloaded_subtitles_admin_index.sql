-- +goose Up
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_downloaded_subtitles_created
    ON downloaded_subtitles (created_at DESC);

CREATE INDEX IF NOT EXISTS idx_downloaded_subtitles_provider_created
    ON downloaded_subtitles (provider, created_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_downloaded_subtitles_provider_created;
DROP INDEX IF EXISTS idx_downloaded_subtitles_created;
-- +goose StatementEnd
