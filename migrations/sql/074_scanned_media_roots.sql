-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS scanned_media_roots (
    media_folder_id INTEGER NOT NULL REFERENCES media_folders(id) ON DELETE CASCADE,
    root_path TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'resolved',
    inferred_type TEXT NOT NULL DEFAULT 'movie',
    type_confidence TEXT NOT NULL DEFAULT 'low',
    title TEXT NOT NULL DEFAULT '',
    year INTEGER NOT NULL DEFAULT 0,
    tmdb_id TEXT NOT NULL DEFAULT '',
    imdb_id TEXT NOT NULL DEFAULT '',
    tvdb_id TEXT NOT NULL DEFAULT '',
    observed_file_count INTEGER NOT NULL DEFAULT 0,
    sample_file_path TEXT NOT NULL DEFAULT '',
    evidence_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    override_source TEXT NOT NULL DEFAULT 'none',
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (media_folder_id, root_path)
);

CREATE INDEX IF NOT EXISTS idx_scanned_media_roots_state_last_seen
    ON scanned_media_roots (media_folder_id, state, last_seen_at DESC);

CREATE INDEX IF NOT EXISTS idx_scanned_media_roots_type_last_seen
    ON scanned_media_roots (media_folder_id, inferred_type, last_seen_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS scanned_media_roots;
-- +goose StatementEnd
