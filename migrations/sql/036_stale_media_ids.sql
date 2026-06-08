-- +goose Up
-- +goose StatementBegin
CREATE TABLE stale_media_ids (
    content_id    TEXT NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE,
    provider      TEXT NOT NULL,
    provider_id   TEXT NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (content_id, provider)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS stale_media_ids;
-- +goose StatementEnd
