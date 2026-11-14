-- +goose Up
-- The login session that started a playback attempt. A plan URL's stream
-- reference (`st`) authorizes the attempt's media on its own only while this
-- session is active, so signing out ends it. Empty for attempts started with
-- an API key or before this column existed; those keep needing the account
-- credential.
ALTER TABLE playback_v3_attempts
    ADD COLUMN login_session_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE playback_v3_attempts
    DROP COLUMN login_session_id;
