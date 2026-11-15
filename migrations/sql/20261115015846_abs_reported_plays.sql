-- Audiobookshelf plays that a client reports only through /session/local
-- (Plappa and Prologue on iOS never call /play) are kept as
-- abs_playback_sessions rows, so the playback lifecycle can list and end them
-- like any other Audiobookshelf play.
--
-- client_session_id is the client's own local session id. It marks a row as a
-- reported play: the server serves nothing for it. reported_listening_base is
-- the client's running listening total when the row began, so a play resumed
-- under a new row credits only what was listened to since.
-- reported_listening_total is the highest running total a report carried: the
-- total only rises within a client session, so a report below it is an older
-- one arriving late and does not move the position.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.abs_playback_sessions
    ADD COLUMN IF NOT EXISTS client_session_id TEXT,
    ADD COLUMN IF NOT EXISTS reported_listening_base DOUBLE PRECISION NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS reported_listening_total DOUBLE PRECISION NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.abs_playback_sessions
    DROP COLUMN IF EXISTS reported_listening_total,
    DROP COLUMN IF EXISTS reported_listening_base,
    DROP COLUMN IF EXISTS client_session_id;
-- +goose StatementEnd
