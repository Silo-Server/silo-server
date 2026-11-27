-- +goose Up
-- A public demo sign-in opens a session bound to the demo purpose with a fixed
-- 24-hour absolute end and no profile binding. Its purpose is enforced from
-- the session row across every API and proxy node, and changing demo.enabled
-- or demo.username immediately revokes all live demo sessions so an off->on or
-- A->B->A toggle can never revive an earlier visitor's tokens.
ALTER TABLE public.auth_sessions
    DROP CONSTRAINT IF EXISTS auth_sessions_purpose_check;
ALTER TABLE public.auth_sessions
    ADD CONSTRAINT auth_sessions_purpose_check CHECK (
        (purpose = 'account' AND bound_profile_id IS NULL)
        OR (purpose = 'remote_playback' AND NULLIF(btrim(bound_profile_id), '') IS NOT NULL
            AND absolute_expires_at IS NOT NULL)
        OR (purpose = 'demo' AND bound_profile_id IS NULL
            AND absolute_expires_at IS NOT NULL)
    );

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.revoke_demo_sessions_on_setting_change() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.key IN ('demo.enabled', 'demo.username') THEN
            UPDATE public.auth_sessions
            SET revoked_at = NOW()
            WHERE purpose = 'demo' AND revoked_at IS NULL;
        END IF;
    ELSIF TG_OP = 'DELETE' THEN
        IF OLD.key IN ('demo.enabled', 'demo.username') THEN
            UPDATE public.auth_sessions
            SET revoked_at = NOW()
            WHERE purpose = 'demo' AND revoked_at IS NULL;
        END IF;
    ELSIF TG_OP = 'UPDATE' THEN
        IF (NEW.key IN ('demo.enabled', 'demo.username') OR OLD.key IN ('demo.enabled', 'demo.username'))
            AND (NEW.key IS DISTINCT FROM OLD.key OR NEW.value IS DISTINCT FROM OLD.value) THEN
            UPDATE public.auth_sessions
            SET revoked_at = NOW()
            WHERE purpose = 'demo' AND revoked_at IS NULL;
        END IF;
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
DROP TRIGGER IF EXISTS server_settings_revoke_demo_sessions ON public.server_settings;
CREATE TRIGGER server_settings_revoke_demo_sessions
    AFTER INSERT OR UPDATE OR DELETE ON public.server_settings
    FOR EACH ROW
    EXECUTE FUNCTION public.revoke_demo_sessions_on_setting_change();

-- +goose Down
-- Convert any demo sessions to revoked account sessions before restoring the
-- prior purpose constraint so rolling back never fails on existing rows and
-- never turns a demo session into an active ordinary account session.
DROP TRIGGER IF EXISTS server_settings_revoke_demo_sessions ON public.server_settings;
DROP FUNCTION IF EXISTS public.revoke_demo_sessions_on_setting_change();
UPDATE public.auth_sessions
SET purpose = 'account',
    revoked_at = COALESCE(revoked_at, NOW())
WHERE purpose = 'demo';
ALTER TABLE public.auth_sessions
    DROP CONSTRAINT IF EXISTS auth_sessions_purpose_check;
ALTER TABLE public.auth_sessions
    ADD CONSTRAINT auth_sessions_purpose_check CHECK (
        (purpose = 'account' AND bound_profile_id IS NULL)
        OR (purpose = 'remote_playback' AND NULLIF(btrim(bound_profile_id), '') IS NOT NULL
            AND absolute_expires_at IS NOT NULL)
    );
