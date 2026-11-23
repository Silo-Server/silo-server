-- +goose Up
CREATE TABLE playback_stop_commits (
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    id uuid NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (user_id, id),
    FOREIGN KEY (user_id, profile_id) REFERENCES user_profiles (user_id, id) ON DELETE CASCADE
);

CREATE INDEX playback_stop_commits_expiry_idx ON playback_stop_commits (user_id, expires_at);

-- +goose Down
DROP TABLE playback_stop_commits;
