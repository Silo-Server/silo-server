-- +goose Up
-- +goose StatementBegin
CREATE TABLE ebook_reader_config (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL,
    content_id TEXT NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, profile_id, content_id)
);

CREATE INDEX ebook_reader_config_profile_updated
    ON ebook_reader_config (user_id, profile_id, updated_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ebook_reader_config;
-- +goose StatementEnd
