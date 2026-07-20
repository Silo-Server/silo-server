-- +goose Up
CREATE TABLE IF NOT EXISTS reading_goals (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL,
    books_per_year INTEGER,
    hours_per_year INTEGER,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, profile_id)
);
CREATE TABLE IF NOT EXISTS reading_achievements (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL,
    achievement_id TEXT NOT NULL,
    achieved_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, profile_id, achievement_id)
);

-- +goose Down
DROP TABLE IF EXISTS reading_achievements;
DROP TABLE IF EXISTS reading_goals;
