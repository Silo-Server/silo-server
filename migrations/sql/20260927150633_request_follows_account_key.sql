-- +goose Up
-- A profile id is only unique within its account: accounts migrated from
-- before profiles all have a profile named 'default'. Key follows by account
-- and profile, so two accounts' profiles with the same id can follow the same
-- title without colliding.
ALTER TABLE media_request_follows DROP CONSTRAINT media_request_follows_pkey;
ALTER TABLE media_request_follows ADD PRIMARY KEY (media_type, tmdb_id, user_id, profile_id);

DROP INDEX media_request_follows_profile_idx;
CREATE INDEX media_request_follows_profile_idx ON media_request_follows (user_id, profile_id);

-- +goose Down
-- The narrower key cannot hold two accounts' follows for one profile id and
-- title; keep the earliest.
DELETE FROM media_request_follows f
USING media_request_follows keep
WHERE f.media_type = keep.media_type
  AND f.tmdb_id = keep.tmdb_id
  AND f.profile_id = keep.profile_id
  AND (keep.created_at, keep.user_id) < (f.created_at, f.user_id);

DROP INDEX media_request_follows_profile_idx;
CREATE INDEX media_request_follows_profile_idx ON media_request_follows (profile_id);

ALTER TABLE media_request_follows DROP CONSTRAINT media_request_follows_pkey;
ALTER TABLE media_request_follows ADD PRIMARY KEY (media_type, tmdb_id, profile_id);
