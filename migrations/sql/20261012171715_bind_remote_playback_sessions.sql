-- +goose Up
-- A remote-playback handoff gives a device a temporary login session for one
-- profile. The session row now records that, so every node enforces it from
-- the row rather than from the tokens it issued:
--   purpose              'account' for every ordinary sign-in, or
--                        'remote_playback' for a handoff.
--   bound_profile_id     the profile the handoff approved; set only on a
--                        remote-playback session.
--   absolute_expires_at  the latest the session may last; a refresh never
--                        moves expires_at past it.
--
-- A constant default is a catalog-only change; no row is rewritten.
ALTER TABLE public.auth_sessions
    ADD COLUMN IF NOT EXISTS purpose text NOT NULL DEFAULT 'account',
    ADD COLUMN IF NOT EXISTS bound_profile_id text,
    ADD COLUMN IF NOT EXISTS absolute_expires_at timestamptz;

-- The trigger, not the application, holds expires_at under the limit, so every
-- writer respects it: this release's refresh, a node from an older release
-- during a rolling deploy (whose refresh sets expires_at to the full refresh
-- lifetime and knows nothing of the limit), and manual SQL.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.cap_auth_session_expiry() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.absolute_expires_at IS NOT NULL AND NEW.expires_at > NEW.absolute_expires_at THEN
        NEW.expires_at := NEW.absolute_expires_at;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS auth_sessions_cap_expiry ON public.auth_sessions;
CREATE TRIGGER auth_sessions_cap_expiry
    BEFORE INSERT OR UPDATE OF expires_at, absolute_expires_at ON public.auth_sessions
    FOR EACH ROW
    EXECUTE FUNCTION public.cap_auth_session_expiry();

-- Collecting a handoff writes the new session's id onto its device request in
-- the same transaction that creates the session. Binding the session from
-- that write also covers a node from an older release, which creates the
-- session as an ordinary sign-in. This release sets the limit itself and the
-- trigger keeps it; for an older node's session the limit is the session's
-- own expiry at collection (the handoff's 24 hours, or the refresh lifetime
-- when that is shorter), and never more than 24 hours after the session was
-- created. A handoff without an approved profile cannot be bound, so its
-- session ends.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.bind_remote_playback_session() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NULLIF(btrim(NEW.approved_profile_id), '') IS NULL THEN
        UPDATE public.auth_sessions SET revoked_at = COALESCE(revoked_at, NOW())
        WHERE id = NEW.auth_session_id;
    ELSE
        UPDATE public.auth_sessions
        SET purpose = 'remote_playback',
            bound_profile_id = btrim(NEW.approved_profile_id),
            absolute_expires_at = COALESCE(absolute_expires_at, LEAST(expires_at, created_at + INTERVAL '24 hours'))
        WHERE id = NEW.auth_session_id;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS device_login_requests_bind_remote_playback ON public.device_login_requests;
CREATE TRIGGER device_login_requests_bind_remote_playback
    AFTER UPDATE OF auth_session_id ON public.device_login_requests
    FOR EACH ROW
    WHEN (NEW.client_purpose = 'remote_playback' AND NEW.auth_session_id IS NOT NULL
        AND NEW.auth_session_id IS DISTINCT FROM OLD.auth_session_id)
    EXECUTE FUNCTION public.bind_remote_playback_session();

-- Handoffs collected before this release were issued as ordinary sign-ins, and
-- a refresh moved them to the full refresh lifetime. Bind the ones whose device
-- request is still stored and end them 24 hours after they were created.
-- Requests are deleted a day after they expire, so a handoff collected before
-- that cannot be told apart from an ordinary sign-in here.
UPDATE public.auth_sessions s
SET purpose = 'remote_playback',
    bound_profile_id = btrim(d.approved_profile_id),
    absolute_expires_at = LEAST(s.expires_at, s.created_at + INTERVAL '24 hours')
FROM public.device_login_requests d
WHERE d.auth_session_id = s.id
    AND d.client_purpose = 'remote_playback'
    AND NULLIF(btrim(d.approved_profile_id), '') IS NOT NULL;

UPDATE public.auth_sessions s
SET revoked_at = COALESCE(s.revoked_at, NOW())
FROM public.device_login_requests d
WHERE d.auth_session_id = s.id
    AND d.client_purpose = 'remote_playback'
    AND NULLIF(btrim(d.approved_profile_id), '') IS NULL;

ALTER TABLE public.auth_sessions
    ADD CONSTRAINT auth_sessions_purpose_check CHECK (
        (purpose = 'account' AND bound_profile_id IS NULL)
        OR (purpose = 'remote_playback' AND NULLIF(btrim(bound_profile_id), '') IS NOT NULL
            AND absolute_expires_at IS NOT NULL)
    );

-- +goose Down
-- Without these columns a remote-playback session would read as an ordinary
-- sign-in of the whole account, so rolling back ends them instead.
UPDATE public.auth_sessions SET revoked_at = COALESCE(revoked_at, NOW())
WHERE purpose = 'remote_playback';
DROP TRIGGER IF EXISTS device_login_requests_bind_remote_playback ON public.device_login_requests;
DROP FUNCTION IF EXISTS public.bind_remote_playback_session();
DROP TRIGGER IF EXISTS auth_sessions_cap_expiry ON public.auth_sessions;
DROP FUNCTION IF EXISTS public.cap_auth_session_expiry();
ALTER TABLE public.auth_sessions
    DROP CONSTRAINT IF EXISTS auth_sessions_purpose_check,
    DROP COLUMN IF EXISTS absolute_expires_at,
    DROP COLUMN IF EXISTS bound_profile_id,
    DROP COLUMN IF EXISTS purpose;
