-- +goose Up
-- +goose StatementBegin
-- External sign-in through OIDC and LDAP auth plugins
-- (docs/architecture/external-sign-in.md). The identity key stays
-- (plugin_installation_id, external_subject).
--
-- An identity row keeps what the provider last said about the person, for
-- the account and admin views, and when it was linked, last used to sign
-- in, and last re-checked ("Provider re-check"):
--   refresh_state          the plugin's opaque state for the next
--                          CheckAccount call (an OIDC refresh token),
--                          AES-GCM encrypted with the server secret and bound
--                          to the row (plugin_auth_identities:refresh_state:<id>);
--                          '' when none.
--   last_check_status      the latest provider answer; '' before the first.
--   last_authenticated_at  when the provider last vouched for the person: an
--                          interactive sign-in (or the linking sign-in that
--                          created the row) or a re-check that answered
--                          active. An unsupported answer revokes the
--                          account's API keys and Audiobookshelf sessions once
--                          this is older than auth.refresh_token_expiry
--                          (linked_at stands in while it is NULL, for an
--                          administrator link).
--   check_outcome_unknown  set, and committed, just before the host calls
--                          the plugin's CheckAccount, and cleared when an
--                          answer showing the plugin's state was current
--                          commits. While it is set, the plugin may have
--                          spent the stored refresh_state at the provider (a
--                          lost answer), so the next refusal of a call that
--                          presented that state is not trusted as a
--                          revocation.
--   pending_refusal        a provider refusal whose revocation rolled back;
--                          the next re-check applies it without asking the
--                          provider again. A sign-in or a recorded answer
--                          clears it.
ALTER TABLE plugin_auth_identities
    ADD COLUMN issuer TEXT NOT NULL DEFAULT '',
    ADD COLUMN username TEXT NOT NULL DEFAULT '',
    ADD COLUMN email TEXT NOT NULL DEFAULT '',
    ADD COLUMN display_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN linked_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ADD COLUMN last_sign_in_at TIMESTAMPTZ,
    ADD COLUMN last_checked_at TIMESTAMPTZ,
    ADD COLUMN last_authenticated_at TIMESTAMPTZ,
    ADD COLUMN refresh_state TEXT NOT NULL DEFAULT '',
    ADD COLUMN last_check_status TEXT NOT NULL DEFAULT '',
    ADD COLUMN check_outcome_unknown BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN pending_refusal TEXT NOT NULL DEFAULT '',
    ADD CONSTRAINT plugin_auth_identities_last_check_status CHECK (last_check_status IN
        ('', 'active', 'not_found', 'disabled', 'not_permitted', 'unsupported', 'unavailable'));

-- Identities linked before this release carry no sign-in or check times, so
-- the absolute-age bound starts at the upgrade for them: otherwise the first
-- scheduled re-check would treat every identity older than the absolute age
-- as stale and delete its account's API keys.
UPDATE plugin_auth_identities SET linked_at = created_at, last_authenticated_at = NOW();

-- A break-glass account is a local admin that keeps password sign-in when
-- the server turns local passwords off. Only admins may hold the flag.
ALTER TABLE users
    ADD COLUMN break_glass BOOLEAN NOT NULL DEFAULT FALSE,
    ADD CONSTRAINT users_break_glass_admin CHECK (NOT break_glass OR role = 'admin');

-- People who pass the provider's group rules get an account on first sign-in
-- unless an administrator turns creation off for the binding.
ALTER TABLE plugin_auth_bindings
    ALTER COLUMN auto_provision SET DEFAULT TRUE;

-- A login session opened through an external sign-in provider remembers the
-- identity it came from; a refresh of that session asks the provider again
-- once the identity's last check is older than auth.provider_recheck_interval.
-- provider_since is when the provider last vouched for the chain of sign-ins
-- the session belongs to: the provider sign-in that opened it, the approving
-- session's value for a device sign-in, or the session's own creation when a
-- link attached it to an identity. NULL for a session the provider never
-- vouched for. When the provider cannot re-check the identity, or the
-- identity is gone (unlinked, plugin removed), the session ends
-- auth.refresh_token_expiry after provider_since instead of sliding.
ALTER TABLE auth_sessions
    ADD COLUMN identity_id BIGINT REFERENCES plugin_auth_identities(id) ON DELETE SET NULL,
    ADD COLUMN provider_since TIMESTAMPTZ;

CREATE INDEX auth_sessions_identity_id_idx ON auth_sessions (identity_id) WHERE identity_id IS NOT NULL;

-- Sessions an auth plugin opened before this release carry no identity, so
-- they would slide forever without a re-check. Attach the open sessions of
-- an account holding exactly one identity to it, as linking an identity
-- does (no account is break-glass yet), so the provider re-checks them and
-- they end at the absolute age when it cannot.
UPDATE auth_sessions s
SET identity_id = i.id, provider_since = s.created_at
FROM plugin_auth_identities i
WHERE i.user_id = s.user_id
    AND s.impersonator_user_id IS NULL
    AND s.revoked_at IS NULL
    AND s.expires_at > NOW()
    AND NOT EXISTS (SELECT 1 FROM plugin_auth_identities j WHERE j.user_id = i.user_id AND j.id <> i.id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS auth_sessions_identity_id_idx;

ALTER TABLE auth_sessions
    DROP COLUMN IF EXISTS provider_since,
    DROP COLUMN IF EXISTS identity_id;

ALTER TABLE plugin_auth_bindings
    ALTER COLUMN auto_provision SET DEFAULT FALSE;

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_break_glass_admin,
    DROP COLUMN IF EXISTS break_glass;

ALTER TABLE plugin_auth_identities
    DROP CONSTRAINT IF EXISTS plugin_auth_identities_last_check_status,
    DROP COLUMN IF EXISTS pending_refusal,
    DROP COLUMN IF EXISTS check_outcome_unknown,
    DROP COLUMN IF EXISTS last_check_status,
    DROP COLUMN IF EXISTS refresh_state,
    DROP COLUMN IF EXISTS last_authenticated_at,
    DROP COLUMN IF EXISTS last_checked_at,
    DROP COLUMN IF EXISTS last_sign_in_at,
    DROP COLUMN IF EXISTS linked_at,
    DROP COLUMN IF EXISTS display_name,
    DROP COLUMN IF EXISTS email,
    DROP COLUMN IF EXISTS username,
    DROP COLUMN IF EXISTS issuer;
-- +goose StatementEnd
