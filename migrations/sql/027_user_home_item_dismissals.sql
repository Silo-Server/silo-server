-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS user_home_item_dismissals (
    user_id INTEGER NOT NULL,
    profile_id TEXT NOT NULL,
    surface TEXT NOT NULL,
    media_item_id TEXT NOT NULL,
    series_id TEXT,
    progress_updated_at TIMESTAMPTZ,
    dismissed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, profile_id, surface, media_item_id),
    CONSTRAINT user_home_item_dismissals_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    CONSTRAINT user_home_item_dismissals_surface_check
        CHECK (surface IN ('continue_watching', 'next_up'))
);

CREATE INDEX IF NOT EXISTS idx_user_home_item_dismissals_lookup
    ON user_home_item_dismissals(user_id, profile_id, surface);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS user_home_item_dismissals;
-- +goose StatementEnd
