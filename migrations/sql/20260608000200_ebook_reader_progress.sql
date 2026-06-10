-- +goose Up
-- +goose StatementBegin
CREATE TABLE ebook_reader_progress (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL,
    content_id TEXT NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE,
    file_id INTEGER NOT NULL REFERENCES media_files(id) ON DELETE CASCADE,
    location TEXT NOT NULL,
    progress DOUBLE PRECISION NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, profile_id, content_id),
    CHECK (progress >= 0 AND progress <= 1)
);

CREATE INDEX ebook_reader_progress_profile_updated
    ON ebook_reader_progress (user_id, profile_id, updated_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ebook_reader_progress;
-- +goose StatementEnd
