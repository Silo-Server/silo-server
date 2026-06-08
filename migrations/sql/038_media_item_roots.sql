-- +goose Up
-- +goose StatementBegin
CREATE TABLE media_item_roots (
    media_folder_id INTEGER NOT NULL REFERENCES media_folders(id) ON DELETE CASCADE,
    canonical_root_path TEXT NOT NULL,
    content_id TEXT NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (media_folder_id, canonical_root_path)
);

CREATE INDEX idx_media_item_roots_content_id
    ON media_item_roots (content_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS media_item_roots;
-- +goose StatementEnd
