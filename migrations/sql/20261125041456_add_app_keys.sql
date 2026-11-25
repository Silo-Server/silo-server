-- +goose Up
-- +goose StatementBegin
-- App keys (docs/architecture/app-keys.md): public signing keys an app holds
-- for one account on this server. The private halves never reach the server.
-- Each public key is the 65-byte uncompressed P-256 point; key_id is the
-- base64url (no padding) of the first 16 bytes of its SHA-256.
--
-- A public key is enrolled once per server: a key row is never deleted while
-- its account exists (revoking it sets revoked_at), so a revoked key cannot
-- be enrolled again. Enrollment also refuses a key held in the other table.
-- Revoking a device key ends the login sessions it opened in the same
-- transaction (auth_sessions.device_key_id).

-- The person key: one per account at a time. It vouches for the account's
-- device keys.
CREATE TABLE public.auth_person_keys (
    id uuid PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    public_key bytea NOT NULL UNIQUE CHECK (octet_length(public_key) = 65),
    key_id text NOT NULL UNIQUE,
    -- The provider identity of the session that enrolled the key, while that
    -- identity still exists.
    identity_id bigint REFERENCES public.plugin_auth_identities(id) ON DELETE SET NULL,
    -- True when the enrolling session came from a provider identity. It stays
    -- true after the identity row is deleted.
    provider_bound boolean NOT NULL DEFAULT false,
    enrolled_via text NOT NULL CHECK (enrolled_via IN ('session', 'app_code')),
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    -- The target of auth_device_keys' account-checked reference.
    CONSTRAINT auth_person_keys_id_user_id_key UNIQUE (id, user_id)
);
CREATE UNIQUE INDEX auth_person_keys_one_live_per_user
    ON public.auth_person_keys (user_id) WHERE revoked_at IS NULL;
CREATE INDEX auth_person_keys_user_id_idx ON public.auth_person_keys (user_id);
CREATE INDEX auth_person_keys_identity_id_idx
    ON public.auth_person_keys (identity_id) WHERE identity_id IS NOT NULL;

-- Device keys: one per app install for an account. A device key opens login
-- sessions.
CREATE TABLE public.auth_device_keys (
    id uuid PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    public_key bytea NOT NULL UNIQUE CHECK (octet_length(public_key) = 65),
    key_id text NOT NULL UNIQUE,
    -- device: an app's own install; app: another app enrolled through a grant.
    kind text NOT NULL CHECK (kind IN ('device', 'app')),
    name text NOT NULL,
    platform text NOT NULL DEFAULT '',
    -- pending and denied hold a key waiting for, or refused, an approval.
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'pending', 'denied')),
    enrolled_via text NOT NULL CHECK (enrolled_via IN ('introduce', 'session', 'app_code', 'grant')),
    -- The person key that signed the introduction or the grant. The
    -- reference includes the account, so a device key can only name a person
    -- key of its own account.
    person_key_id uuid,
    identity_id bigint REFERENCES public.plugin_auth_identities(id) ON DELETE SET NULL,
    provider_bound boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    revoked_at timestamptz,
    -- The target of auth_sessions' account-checked reference.
    CONSTRAINT auth_device_keys_id_user_id_key UNIQUE (id, user_id),
    CONSTRAINT auth_device_keys_person_key_fkey FOREIGN KEY (person_key_id, user_id)
        REFERENCES public.auth_person_keys (id, user_id) ON DELETE SET NULL (person_key_id)
);
CREATE INDEX auth_device_keys_user_id_idx ON public.auth_device_keys (user_id);
CREATE INDEX auth_device_keys_person_key_id_idx
    ON public.auth_device_keys (person_key_id) WHERE person_key_id IS NOT NULL;
CREATE INDEX auth_device_keys_identity_id_idx
    ON public.auth_device_keys (identity_id) WHERE identity_id IS NOT NULL;

-- The device key that opened a login session; NULL for every other sign-in.
-- The reference includes the account, so a session can only name a device
-- key of its own account.
ALTER TABLE public.auth_sessions
    ADD COLUMN device_key_id uuid,
    ADD CONSTRAINT auth_sessions_device_key_fkey FOREIGN KEY (device_key_id, user_id)
        REFERENCES public.auth_device_keys (id, user_id) ON DELETE CASCADE;
CREATE INDEX auth_sessions_device_key_id_idx
    ON public.auth_sessions (device_key_id) WHERE device_key_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.auth_sessions_device_key_id_idx;
ALTER TABLE public.auth_sessions DROP COLUMN IF EXISTS device_key_id;
DROP TABLE IF EXISTS public.auth_device_keys;
DROP TABLE IF EXISTS public.auth_person_keys;
-- +goose StatementEnd
