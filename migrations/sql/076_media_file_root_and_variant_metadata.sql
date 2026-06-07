-- +goose Up
-- +goose StatementBegin
ALTER TABLE media_files
    ADD COLUMN IF NOT EXISTS canonical_root_path TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS edition_raw TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS edition_key TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS edition_confidence DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS edition_source TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS presentation_kind TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS presentation_group_key TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS presentation_part_index INTEGER,
    ADD COLUMN IF NOT EXISTS presentation_part_total INTEGER,
    ADD COLUMN IF NOT EXISTS multi_episode_start INTEGER,
    ADD COLUMN IF NOT EXISTS multi_episode_end INTEGER;

CREATE INDEX IF NOT EXISTS idx_media_files_folder_root
    ON media_files (media_folder_id, canonical_root_path);

CREATE INDEX IF NOT EXISTS idx_media_files_variant_group
    ON media_files (content_id, edition_key, presentation_group_key, resolution);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_media_files_variant_group;
DROP INDEX IF EXISTS idx_media_files_folder_root;

ALTER TABLE media_files
    DROP COLUMN IF EXISTS multi_episode_end,
    DROP COLUMN IF EXISTS multi_episode_start,
    DROP COLUMN IF EXISTS presentation_part_total,
    DROP COLUMN IF EXISTS presentation_part_index,
    DROP COLUMN IF EXISTS presentation_group_key,
    DROP COLUMN IF EXISTS presentation_kind,
    DROP COLUMN IF EXISTS edition_source,
    DROP COLUMN IF EXISTS edition_confidence,
    DROP COLUMN IF EXISTS edition_key,
    DROP COLUMN IF EXISTS edition_raw,
    DROP COLUMN IF EXISTS canonical_root_path;
-- +goose StatementEnd
