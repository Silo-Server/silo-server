-- +goose Up
-- A shuffle plays random movies and episodes from one scope (a library, a
-- series, a season, or a collection) until the profile stops. The row holds
-- the item playing now and the one picked to play next, so any API process can
-- continue a shuffle another one started.
CREATE TABLE playback_shuffles (
    id uuid PRIMARY KEY,
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    scope_kind text NOT NULL,
    scope_id text NOT NULL,
    -- The scope's name when the shuffle started, and for a season its series.
    scope_title text NOT NULL,
    scope_parent_title text NOT NULL DEFAULT '',
    current_content_id text NOT NULL,
    next_content_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT playback_shuffles_scope_kind_check
        CHECK (scope_kind IN ('library', 'series', 'season', 'library_collection', 'user_collection')),
    CONSTRAINT playback_shuffles_profile_fkey FOREIGN KEY (user_id, profile_id)
        REFERENCES user_profiles (user_id, id) ON DELETE CASCADE
);

-- Abandoned shuffles are deleted by age.
CREATE INDEX playback_shuffles_updated_at_idx ON playback_shuffles (updated_at);
CREATE INDEX playback_shuffles_profile_idx ON playback_shuffles (profile_id);

-- Items a shuffle has already handed out in its current cycle. A pick skips
-- them, so nothing repeats until the whole scope has played.
CREATE TABLE playback_shuffle_items (
    shuffle_id uuid NOT NULL REFERENCES playback_shuffles (id) ON DELETE CASCADE,
    content_id text NOT NULL,
    PRIMARY KEY (shuffle_id, content_id)
);

-- +goose Down
DROP TABLE playback_shuffle_items;
DROP TABLE playback_shuffles;
