-- +goose Up
-- A login session opened by device sign-in (a code shown on a TV or other
-- device and approved from a signed-in session, or a Jellyfin Quick Connect
-- request) may sit on a device the account holder doesn't control. The row now
-- records that, so the session list and revocation can limit such a session to
-- itself. A column without a default is a catalog-only change; no row is
-- rewritten.
ALTER TABLE public.auth_sessions
    ADD COLUMN IF NOT EXISTS via_device_sign_in boolean;

COMMENT ON COLUMN public.auth_sessions.via_device_sign_in IS
    'true: a device sign-in or Quick Connect request opened the session. false: any other sign-in. NULL: the session was opened before this column, or by a server of an older release during a rolling deploy.';

-- Sessions whose device request is still stored were opened by device sign-in.
-- Requests are deleted about a day after they expire, so an older device
-- session keeps NULL and is judged by its recorded platform.
UPDATE public.auth_sessions s
SET via_device_sign_in = TRUE
FROM public.device_login_requests d
WHERE d.auth_session_id = s.id;

-- +goose Down
ALTER TABLE public.auth_sessions
    DROP COLUMN IF EXISTS via_device_sign_in;
