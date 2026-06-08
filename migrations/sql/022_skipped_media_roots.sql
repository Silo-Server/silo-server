-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS skipped_media_roots (
    media_folder_id INTEGER NOT NULL REFERENCES media_folders(id) ON DELETE CASCADE,
    root_path TEXT NOT NULL,
    reason TEXT NOT NULL,
    sample_file_path TEXT NOT NULL DEFAULT '',
    file_count INTEGER NOT NULL DEFAULT 0,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (media_folder_id, root_path)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS skipped_media_roots;
-- +goose StatementEnd
