-- +goose Up
-- Rows are provisioned only by a separately reviewed source registration flow.
-- Creating this table grants no account permission to use an exact source.
CREATE TABLE public.playback_source_markers (
    user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    source_id UUID NOT NULL,
    selection_generation BIGINT NOT NULL CHECK (selection_generation > 0),
    gate TEXT NOT NULL DEFAULT 'quarantined' CHECK (gate IN ('writable', 'quarantined', 'sealed'))
);

-- +goose Down
DROP TABLE public.playback_source_markers;
