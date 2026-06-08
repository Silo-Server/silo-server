-- +goose Up
-- +goose StatementBegin
ALTER TABLE media_files
    ADD COLUMN IF NOT EXISTS observed_root_path TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS content_group_key TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS group_key_version INTEGER NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS base_title TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS base_year INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS base_type TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS identity_confidence TEXT NOT NULL DEFAULT 'low',
    ADD COLUMN IF NOT EXISTS identity_json JSONB NOT NULL DEFAULT '{}'::jsonb;

CREATE INDEX IF NOT EXISTS idx_media_files_folder_group
    ON media_files (media_folder_id, group_key_version, content_group_key);

CREATE INDEX IF NOT EXISTS idx_media_files_folder_observed_root
    ON media_files (media_folder_id, observed_root_path);

CREATE TABLE IF NOT EXISTS scanned_media_groups (
    media_folder_id INTEGER NOT NULL REFERENCES media_folders(id) ON DELETE CASCADE,
    group_key_version INTEGER NOT NULL,
    content_group_key TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'resolved',
    inferred_type TEXT NOT NULL DEFAULT '',
    type_confidence TEXT NOT NULL DEFAULT 'low',
    base_title TEXT NOT NULL DEFAULT '',
    base_year INTEGER NOT NULL DEFAULT 0,
    tmdb_id TEXT NOT NULL DEFAULT '',
    imdb_id TEXT NOT NULL DEFAULT '',
    tvdb_id TEXT NOT NULL DEFAULT '',
    observed_file_count INTEGER NOT NULL DEFAULT 0,
    sample_file_path TEXT NOT NULL DEFAULT '',
    sample_observed_root_path TEXT NOT NULL DEFAULT '',
    evidence_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    override_source TEXT NOT NULL DEFAULT 'none',
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (media_folder_id, group_key_version, content_group_key)
);

CREATE INDEX IF NOT EXISTS idx_scanned_media_groups_state_last_seen
    ON scanned_media_groups (media_folder_id, state, last_seen_at DESC);

CREATE TABLE IF NOT EXISTS observed_media_locations (
    media_folder_id INTEGER NOT NULL REFERENCES media_folders(id) ON DELETE CASCADE,
    observed_root_path TEXT NOT NULL,
    location_type TEXT NOT NULL DEFAULT '',
    sample_file_path TEXT NOT NULL DEFAULT '',
    observed_file_count INTEGER NOT NULL DEFAULT 0,
    content_group_count INTEGER NOT NULL DEFAULT 0,
    primary_group_key_version INTEGER NOT NULL DEFAULT 0,
    primary_content_group_key TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'resolved',
    evidence_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (media_folder_id, observed_root_path)
);

CREATE TABLE IF NOT EXISTS media_group_locations (
    media_folder_id INTEGER NOT NULL REFERENCES media_folders(id) ON DELETE CASCADE,
    group_key_version INTEGER NOT NULL,
    content_group_key TEXT NOT NULL,
    observed_root_path TEXT NOT NULL,
    is_primary BOOLEAN NOT NULL DEFAULT false,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (media_folder_id, group_key_version, content_group_key, observed_root_path)
);

CREATE INDEX IF NOT EXISTS idx_media_group_locations_observed_root
    ON media_group_locations (media_folder_id, observed_root_path);

CREATE TABLE IF NOT EXISTS media_item_groups (
    media_folder_id INTEGER NOT NULL REFERENCES media_folders(id) ON DELETE CASCADE,
    group_key_version INTEGER NOT NULL,
    content_group_key TEXT NOT NULL,
    content_id TEXT NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (media_folder_id, group_key_version, content_group_key)
);

CREATE INDEX IF NOT EXISTS idx_media_item_groups_content_id
    ON media_item_groups (content_id);

CREATE TABLE IF NOT EXISTS media_group_overrides (
    media_folder_id INTEGER NOT NULL REFERENCES media_folders(id) ON DELETE CASCADE,
    group_key_version INTEGER NOT NULL,
    content_group_key TEXT NOT NULL,
    forced_type TEXT NOT NULL DEFAULT '',
    forced_title TEXT NOT NULL DEFAULT '',
    forced_year INTEGER NOT NULL DEFAULT 0,
    forced_tmdb_id TEXT NOT NULL DEFAULT '',
    forced_imdb_id TEXT NOT NULL DEFAULT '',
    forced_tvdb_id TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    created_by_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    updated_by_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (media_folder_id, group_key_version, content_group_key)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS media_group_overrides;
DROP TABLE IF EXISTS media_item_groups;
DROP TABLE IF EXISTS media_group_locations;
DROP TABLE IF EXISTS observed_media_locations;
DROP TABLE IF EXISTS scanned_media_groups;

DROP INDEX IF EXISTS idx_media_files_folder_observed_root;
DROP INDEX IF EXISTS idx_media_files_folder_group;

ALTER TABLE media_files
    DROP COLUMN IF EXISTS identity_json,
    DROP COLUMN IF EXISTS identity_confidence,
    DROP COLUMN IF EXISTS base_type,
    DROP COLUMN IF EXISTS base_year,
    DROP COLUMN IF EXISTS base_title,
    DROP COLUMN IF EXISTS group_key_version,
    DROP COLUMN IF EXISTS content_group_key,
    DROP COLUMN IF EXISTS observed_root_path;
-- +goose StatementEnd
