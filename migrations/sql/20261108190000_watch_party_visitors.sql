-- +goose Up
-- A Watch Party host can invite people who have no Silo account. An invite is
-- a link to one room; a visitor who opens it gets a login session of the
-- host's account that only reaches that room and the room's selected file:
--   watch_together_visitor_invites  the links a host created; the token is
--                                   stored only as its SHA-256 digest.
--   watch_together_visitors         who joined through a link, under which
--                                   name, and with which login session.
--   auth_sessions                   purpose 'watch_party_visitor' binds a
--                                   session to the host's profile, one room
--                                   and one visitor, and requires an end.
CREATE TABLE IF NOT EXISTS public.watch_together_visitor_invites (
    id                  text PRIMARY KEY,
    room_id             text NOT NULL REFERENCES public.watch_together_rooms(id) ON DELETE CASCADE,
    token_digest        bytea NOT NULL UNIQUE,
    created_by_user_id  integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    created_by_profile_id text NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT NOW(),
    expires_at          timestamptz NOT NULL,
    revoked_at          timestamptz
);

CREATE INDEX IF NOT EXISTS idx_watch_together_visitor_invites_room
    ON public.watch_together_visitor_invites (room_id);

CREATE TABLE IF NOT EXISTS public.watch_together_visitors (
    id              text PRIMARY KEY,
    room_id         text NOT NULL REFERENCES public.watch_together_rooms(id) ON DELETE CASCADE,
    invite_id       text NOT NULL REFERENCES public.watch_together_visitor_invites(id) ON DELETE CASCADE,
    display_name    text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 40),
    auth_session_id text NOT NULL UNIQUE,
    created_at      timestamptz NOT NULL DEFAULT NOW(),
    removed_at      timestamptz
);

CREATE INDEX IF NOT EXISTS idx_watch_together_visitors_room
    ON public.watch_together_visitors (room_id);
CREATE INDEX IF NOT EXISTS idx_watch_together_visitors_invite
    ON public.watch_together_visitors (invite_id);

ALTER TABLE public.auth_sessions
    ADD COLUMN IF NOT EXISTS bound_room_id text,
    ADD COLUMN IF NOT EXISTS bound_visitor_id text;

ALTER TABLE public.auth_sessions
    DROP CONSTRAINT IF EXISTS auth_sessions_purpose_check;
ALTER TABLE public.auth_sessions
    ADD CONSTRAINT auth_sessions_purpose_check CHECK (
        (purpose = 'account' AND bound_profile_id IS NULL
            AND bound_room_id IS NULL AND bound_visitor_id IS NULL)
        OR (purpose = 'remote_playback' AND NULLIF(btrim(bound_profile_id), '') IS NOT NULL
            AND absolute_expires_at IS NOT NULL
            AND bound_room_id IS NULL AND bound_visitor_id IS NULL)
        OR (purpose = 'watch_party_visitor' AND NULLIF(btrim(bound_profile_id), '') IS NOT NULL
            AND absolute_expires_at IS NOT NULL
            AND NULLIF(btrim(bound_room_id), '') IS NOT NULL
            AND NULLIF(btrim(bound_visitor_id), '') IS NOT NULL)
    );

-- +goose Down
-- Without the binding columns a visitor session would read as an ordinary
-- sign-in of the host's account, so rolling back ends them first.
UPDATE public.auth_sessions SET revoked_at = COALESCE(revoked_at, NOW())
WHERE purpose = 'watch_party_visitor';
DELETE FROM public.auth_sessions WHERE purpose = 'watch_party_visitor';
ALTER TABLE public.auth_sessions
    DROP CONSTRAINT IF EXISTS auth_sessions_purpose_check;
ALTER TABLE public.auth_sessions
    DROP COLUMN IF EXISTS bound_visitor_id,
    DROP COLUMN IF EXISTS bound_room_id;
ALTER TABLE public.auth_sessions
    ADD CONSTRAINT auth_sessions_purpose_check CHECK (
        (purpose = 'account' AND bound_profile_id IS NULL)
        OR (purpose = 'remote_playback' AND NULLIF(btrim(bound_profile_id), '') IS NOT NULL
            AND absolute_expires_at IS NOT NULL)
    );
DROP TABLE IF EXISTS public.watch_together_visitors;
DROP TABLE IF EXISTS public.watch_together_visitor_invites;
