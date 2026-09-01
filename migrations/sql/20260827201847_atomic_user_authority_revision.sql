-- +goose Up
-- auth_revision is a monotonic account-authentication epoch. Sessions snapshot
-- it at creation and are accepted only while their snapshot still matches the
-- account. This closes races where a login or impersonation session is inserted
-- after an administrative revocation statement has already run.
ALTER TABLE public.users
    ADD COLUMN auth_revision bigint NOT NULL DEFAULT 1;

ALTER TABLE public.auth_sessions
    ADD COLUMN auth_revision bigint,
    ADD COLUMN impersonator_auth_revision bigint;

UPDATE public.auth_sessions AS session
SET auth_revision = account.auth_revision
FROM public.users AS account
WHERE account.id = session.user_id;

UPDATE public.auth_sessions AS session
SET impersonator_auth_revision = account.auth_revision
FROM public.users AS account
WHERE account.id = session.impersonator_user_id;

ALTER TABLE public.auth_sessions
    ALTER COLUMN auth_revision SET NOT NULL;

-- Device-login approval is a delegated credential grant. Bind it to the
-- approving native session so a later poll cannot outlive that authority or
-- turn an impersonated session into an ordinary one.
ALTER TABLE public.device_login_requests
    ADD COLUMN approved_by_session_id text,
    ADD CONSTRAINT device_login_requests_approved_session_fkey
        FOREIGN KEY (approved_by_session_id)
        REFERENCES public.auth_sessions(id)
        ON DELETE SET NULL;

-- Legacy compat rows contain encrypted native tokens, so SQL cannot safely
-- recover their session IDs. Expire them once rather than carry unverifiable
-- ten-year bearer credentials across the new authority boundary.
DELETE FROM public.jellycompat_sessions;

ALTER TABLE public.jellycompat_sessions
    ADD COLUMN streamapp_session_id text NOT NULL,
    ADD COLUMN auth_revision bigint NOT NULL,
    ADD CONSTRAINT jellycompat_sessions_auth_session_fkey
        FOREIGN KEY (streamapp_session_id)
        REFERENCES public.auth_sessions(id)
        ON DELETE CASCADE,
    ADD CONSTRAINT jellycompat_sessions_user_fkey
        FOREIGN KEY (streamapp_user_id)
        REFERENCES public.users(id)
        ON DELETE CASCADE;

-- +goose Down
ALTER TABLE public.device_login_requests
    DROP CONSTRAINT IF EXISTS device_login_requests_approved_session_fkey,
    DROP COLUMN IF EXISTS approved_by_session_id;

ALTER TABLE public.jellycompat_sessions
    DROP CONSTRAINT IF EXISTS jellycompat_sessions_user_fkey,
    DROP CONSTRAINT IF EXISTS jellycompat_sessions_auth_session_fkey,
    DROP COLUMN IF EXISTS auth_revision,
    DROP COLUMN IF EXISTS streamapp_session_id;

ALTER TABLE public.auth_sessions
    DROP COLUMN IF EXISTS impersonator_auth_revision,
    DROP COLUMN IF EXISTS auth_revision;

ALTER TABLE public.users
    DROP COLUMN IF EXISTS auth_revision;
