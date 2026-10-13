-- +goose Up
-- Profiles that asked to be told when a requested title becomes available.
-- Keyed by title rather than by request so a follow survives the request
-- failing and being requested again. The requester needs no row: the
-- fulfilled notification always reaches them.
CREATE TABLE media_request_follows (
    media_type text NOT NULL,
    tmdb_id integer NOT NULL,
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (media_type, tmdb_id, profile_id),
    CONSTRAINT media_request_follows_media_type_check CHECK (media_type IN ('movie', 'series')),
    CONSTRAINT media_request_follows_tmdb_positive CHECK (tmdb_id > 0),
    CONSTRAINT media_request_follows_profile_fkey FOREIGN KEY (user_id, profile_id)
        REFERENCES user_profiles (user_id, id) ON DELETE CASCADE
);

CREATE INDEX media_request_follows_profile_idx ON media_request_follows (profile_id);

-- +goose Down
DROP TABLE media_request_follows;
