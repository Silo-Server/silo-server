-- +goose Up
-- +goose StatementBegin
-- last_used_at on a device key means the last time it opened a login
-- session. A key waiting for an administrator's approval never opens one, so
-- its sign-in attempts, which the five-second retry rule counts, have a
-- column of their own. It is never shown.
ALTER TABLE public.auth_device_keys ADD COLUMN last_attempt_at timestamptz;

-- A key bound to a provider identity can open a session only through that
-- identity. Once the identity row is deleted the key can never sign in again
-- (a new provider sign-in makes a new identity), so it is revoked, and the
-- sessions its device keys opened end with it. This is a trigger rather than
-- application code because it must hold on every path that deletes an
-- identity: unlinking, the cascade from uninstalling a plugin, and a node
-- running an older release. It runs before the delete, while the identity row
-- is locked by it, so its locks follow the order key sign-in takes: the
-- installation, the identity, the key rows, then the sessions.
CREATE FUNCTION public.revoke_app_keys_of_deleted_identity() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    UPDATE public.auth_person_keys SET revoked_at = COALESCE(revoked_at, NOW())
        WHERE identity_id = OLD.id AND revoked_at IS NULL;
    WITH revoked AS (
        UPDATE public.auth_device_keys SET revoked_at = COALESCE(revoked_at, NOW())
            WHERE identity_id = OLD.id AND revoked_at IS NULL
            RETURNING id
    )
    UPDATE public.auth_sessions SET revoked_at = COALESCE(revoked_at, NOW())
        WHERE device_key_id IN (SELECT id FROM revoked) AND revoked_at IS NULL;
    RETURN OLD;
END;
$$;

CREATE TRIGGER revoke_app_keys_of_deleted_identity
    BEFORE DELETE ON public.plugin_auth_identities
    FOR EACH ROW EXECUTE FUNCTION public.revoke_app_keys_of_deleted_identity();
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS revoke_app_keys_of_deleted_identity ON public.plugin_auth_identities;
DROP FUNCTION IF EXISTS public.revoke_app_keys_of_deleted_identity();
ALTER TABLE public.auth_device_keys DROP COLUMN IF EXISTS last_attempt_at;
-- +goose StatementEnd
