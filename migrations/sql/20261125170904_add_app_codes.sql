-- +goose Up
-- +goose StatementBegin
-- App codes (docs/architecture/app-keys.md, "App codes"): one-time codes a
-- server's web page hands to the Silo app, which redeems one with a signed
-- request to enroll its keys. Only the SHA-256 of a code is stored. A code
-- lives ten minutes, never past its invitation's expiry; the retention step
-- deletes rows past expires_at, spent or not.
--
-- A join code belongs to an invitation and reserves the account name the
-- person chose; no account exists until the app redeems it. A session code
-- belongs to an existing account and records the provider binding of the
-- signed-in session that made it, which the keys it enrolls take.
CREATE TABLE public.auth_app_codes (
    code_hash bytea PRIMARY KEY CHECK (octet_length(code_hash) = 32),
    kind text NOT NULL CONSTRAINT auth_app_codes_kind_check CHECK (kind IN ('join', 'session')),
    user_id integer REFERENCES public.users(id) ON DELETE CASCADE,
    invitation_id bigint REFERENCES public.invitations(id) ON DELETE CASCADE,
    -- A join's account name and optional email, reserved while the code is
    -- unspent. The reservation is advisory: the account insert at redeem
    -- decides, and a name taken in between refuses the redeem.
    reserved_username citext,
    reserved_email citext,
    -- A session code: the session that made it, which must still be live at
    -- redeem, and its provider binding. No reference constraints: deleting a
    -- session or an identity never writes this table.
    session_id text,
    identity_id bigint,
    provider_bound boolean NOT NULL DEFAULT false,
    expires_at timestamptz NOT NULL,
    redeemed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT auth_app_codes_target_check CHECK (
        (kind = 'join' AND invitation_id IS NOT NULL AND user_id IS NULL AND reserved_username IS NOT NULL)
        OR (kind <> 'join' AND user_id IS NOT NULL AND invitation_id IS NULL AND reserved_username IS NULL)
    )
);
-- One unspent code per invitation (a new one replaces it) and per reserved
-- name. An expired unspent row keeps its slot until the issuing code clears
-- it or the retention step deletes it.
CREATE UNIQUE INDEX auth_app_codes_invitation_live_idx ON public.auth_app_codes (invitation_id)
    WHERE redeemed_at IS NULL AND invitation_id IS NOT NULL;
CREATE UNIQUE INDEX auth_app_codes_username_live_idx ON public.auth_app_codes (reserved_username)
    WHERE redeemed_at IS NULL AND reserved_username IS NOT NULL;
CREATE INDEX auth_app_codes_user_id_idx ON public.auth_app_codes (user_id) WHERE user_id IS NOT NULL;
CREATE INDEX auth_app_codes_expires_at_idx ON public.auth_app_codes (expires_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.auth_app_codes;
-- +goose StatementEnd
