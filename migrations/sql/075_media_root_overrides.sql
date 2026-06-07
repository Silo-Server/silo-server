-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS media_root_overrides (
    media_folder_id INTEGER NOT NULL REFERENCES media_folders(id) ON DELETE CASCADE,
    root_path TEXT NOT NULL,
    forced_type TEXT NOT NULL DEFAULT '',
    forced_title TEXT NOT NULL DEFAULT '',
    forced_year INTEGER NOT NULL DEFAULT 0,
    forced_tmdb_id TEXT NOT NULL DEFAULT '',
    forced_imdb_id TEXT NOT NULL DEFAULT '',
    forced_tvdb_id TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    created_by_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    updated_by_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (media_folder_id, root_path)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS media_root_overrides;
-- +goose StatementEnd
