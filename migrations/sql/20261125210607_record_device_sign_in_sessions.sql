-- +goose Up
-- A login session opened by device sign-in (a code shown on a TV or other
-- device and approved from a signed-in session, a Jellyfin Quick Connect
-- request, or a remote-playback handoff) may sit on a device the account
-- holder doesn't control. The row now records that, so the session list and
-- revocation can limit such a session to itself. A column without a default
-- is a catalog-only change; no row is rewritten. The next migration marks the
-- sessions opened before this one, in its own transaction.
ALTER TABLE public.auth_sessions
    ADD COLUMN IF NOT EXISTS via_device_sign_in boolean;

COMMENT ON COLUMN public.auth_sessions.via_device_sign_in IS
    'true: a device sign-in, Quick Connect or remote-playback handoff request opened the session. false: any other sign-in. NULL: the session was opened before this column, or by a server of an older release during a rolling deploy.';

-- +goose Down
ALTER TABLE public.auth_sessions
    DROP COLUMN IF EXISTS via_device_sign_in;
