-- +goose Up
-- +goose StatementBegin
-- Per-profile onboarding-tour state for the Postgres user-store backend
-- (the SQLite backend carries the same table via userdb schema v14).
-- Keyed by tour_id so a future materially-different tour can re-prompt
-- without clobbering history. Timestamps are text RFC3339 to mirror the
-- SQLite store's representation through the shared UserStore interface.
CREATE TABLE IF NOT EXISTS user_profile_onboarding (
    user_id INTEGER NOT NULL,
    profile_id TEXT NOT NULL,
    tour_id TEXT NOT NULL,
    last_step TEXT NOT NULL DEFAULT '',
    completed_at TEXT,
    skipped_at TEXT,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (user_id, profile_id, tour_id),
    CONSTRAINT user_profile_onboarding_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS user_profile_onboarding;
-- +goose StatementEnd
