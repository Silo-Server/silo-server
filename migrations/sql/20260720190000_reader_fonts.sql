-- +goose Up
CREATE TABLE IF NOT EXISTS reader_fonts (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL,
    name TEXT NOT NULL,
    filename TEXT NOT NULL,
    format TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_reader_fonts_owner ON reader_fonts (user_id, profile_id);

-- +goose Down
DROP TABLE IF EXISTS reader_fonts;
