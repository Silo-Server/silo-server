-- +goose Up
-- +goose StatementBegin
-- last_used_at on a device key means the last time it opened a login
-- session. A key waiting for an administrator's approval never opens one, so
-- its sign-in attempts, which the five-second retry rule counts, have a
-- column of their own. It is never shown.
ALTER TABLE public.auth_device_keys ADD COLUMN last_attempt_at timestamptz;

-- A key's identity_id names the provider identity it is bound to, as a plain
-- number with no foreign key: deleting an identity (unlinking it, or the
-- cascade from uninstalling its plugin) must never write to a key row, since
-- application transactions write key rows under the account lock, and a
-- cascade does not take it. Identity IDs come from a sequence and are not
-- reused, and every lookup of a key's identity compares the account too, so
-- the number can only ever name that identity. A provider-bound key whose
-- identity is gone is revoked by the application: at unlink, or at the key's
-- next use (docs/architecture/app-keys.md, "When an identity is deleted").
ALTER TABLE public.auth_person_keys DROP CONSTRAINT IF EXISTS auth_person_keys_identity_id_fkey;
ALTER TABLE public.auth_device_keys DROP CONSTRAINT IF EXISTS auth_device_keys_identity_id_fkey;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE public.auth_person_keys k SET identity_id = NULL
    WHERE identity_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM public.plugin_auth_identities i WHERE i.id = k.identity_id);
UPDATE public.auth_device_keys k SET identity_id = NULL
    WHERE identity_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM public.plugin_auth_identities i WHERE i.id = k.identity_id);
ALTER TABLE public.auth_person_keys ADD CONSTRAINT auth_person_keys_identity_id_fkey
    FOREIGN KEY (identity_id) REFERENCES public.plugin_auth_identities(id) ON DELETE SET NULL;
ALTER TABLE public.auth_device_keys ADD CONSTRAINT auth_device_keys_identity_id_fkey
    FOREIGN KEY (identity_id) REFERENCES public.plugin_auth_identities(id) ON DELETE SET NULL;
ALTER TABLE public.auth_device_keys DROP COLUMN IF EXISTS last_attempt_at;
-- +goose StatementEnd
