-- +goose Up
-- +goose StatementBegin
-- Remote promotional/supplemental videos (trailers, teasers, featurettes, ...)
-- discovered by metadata providers during match/refresh. Rows are replaced
-- wholesale per refresh (item_people pattern); the (content_id, provider,
-- provider_key) key dedupes re-fetches of the same provider video.
CREATE TABLE item_videos (
    id BIGINT PRIMARY KEY,
    content_id TEXT NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE,
    provider TEXT NOT NULL,
    provider_key TEXT NOT NULL,
    -- Lowercase snake_case kind vocabulary shared with media_extras.kind:
    -- trailer, teaser, featurette, clip, behind_the_scenes, bloopers, other.
    -- Deliberately CHECK-free: unknown provider kinds normalize to 'other' in
    -- code, and new kinds must not require a migration.
    kind TEXT NOT NULL,
    site TEXT NOT NULL DEFAULT 'youtube',
    site_key TEXT NOT NULL,
    name TEXT NOT NULL DEFAULT '',
    language TEXT NOT NULL DEFAULT '',
    is_official BOOLEAN NOT NULL DEFAULT FALSE,
    size_hint INTEGER NOT NULL DEFAULT 0,
    published_at TIMESTAMPTZ,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (content_id, provider, provider_key)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_item_videos_content_id ON item_videos (content_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS item_videos;
-- +goose StatementEnd
