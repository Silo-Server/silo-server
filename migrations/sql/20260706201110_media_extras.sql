-- +goose Up
-- +goose StatementBegin
-- Local extras (trailers, featurettes, behind-the-scenes, deleted scenes, ...)
-- discovered by the scanner alongside a movie or under a series root. Follows
-- the episodes precedent: an extra is a playable child entity with its own
-- content_id (minted via contentid.ForLocal on the file path) resolved through
-- GetWatchDetail's fallback chain, gated by the parent item's library access.
CREATE TABLE media_extras (
    content_id TEXT PRIMARY KEY,
    parent_id TEXT NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE,
    -- Shared kind vocabulary with item_videos.kind, plus local-only
    -- 'deleted_scene'. CHECK-free on purpose (see item_videos).
    kind TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX idx_media_extras_parent_id ON media_extras (parent_id);
-- +goose StatementEnd

-- +goose StatementBegin
-- Extras files keep content_id/episode_id NULL so every existing
-- content-keyed query (version picker, playback variants, downloads) is
-- structurally blind to them; ownership flows through extra_id instead.
ALTER TABLE media_files
    ADD COLUMN IF NOT EXISTS extra_id TEXT;
-- +goose StatementEnd

-- +goose StatementBegin
-- NOT VALID keeps the constraint addition from scanning the (large)
-- media_files table under an exclusive lock; VALIDATE below only takes
-- SHARE UPDATE EXCLUSIVE and the column is all-NULL at migration time.
ALTER TABLE media_files
    ADD CONSTRAINT media_files_extra_id_fkey
    FOREIGN KEY (extra_id) REFERENCES media_extras(content_id) ON DELETE SET NULL
    NOT VALID;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE media_files VALIDATE CONSTRAINT media_files_extra_id_fkey;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_media_files_extra_id
    ON media_files (extra_id) WHERE extra_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_media_files_extra_id;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE media_files DROP COLUMN IF EXISTS extra_id;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE IF EXISTS media_extras;
-- +goose StatementEnd
