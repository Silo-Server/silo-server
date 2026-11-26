-- +goose Up
-- +goose StatementBegin
-- Sign-in links (docs/architecture/app-keys.md, "Sign-in links"): a
-- single-use link an administrator makes for one account, so a person who
-- lost every device gets back in. Redeeming the link trades it for an app
-- code of kind sign_in_link and, while local password sign-in is on, a
-- set-password token that lives an hour, never past the link. Only SHA-256
-- digests of the tokens are stored. A new link replaces the account's
-- unredeemed ones; the retention step deletes rows once both tokens are past
-- their expiry. Its own table, not password_reset_tokens: that table holds
-- one link per account bound to its current password, which an account with
-- no password doesn't have, and a sign-in link must not cancel a reset link
-- or the reverse.
CREATE TABLE public.auth_sign_in_links (
    token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash) = 32),
    user_id integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    created_by integer REFERENCES public.users(id) ON DELETE SET NULL,
    expires_at timestamptz NOT NULL,
    redeemed_at timestamptz,
    set_password_hash bytea UNIQUE CHECK (octet_length(set_password_hash) = 32),
    set_password_expires_at timestamptz,
    set_password_used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT auth_sign_in_links_set_password_check CHECK (
        (set_password_hash IS NULL) = (set_password_expires_at IS NULL)
        AND (set_password_hash IS NULL OR redeemed_at IS NOT NULL)
    )
);
CREATE INDEX auth_sign_in_links_user_id_idx ON public.auth_sign_in_links (user_id);
CREATE INDEX auth_sign_in_links_expires_at_idx ON public.auth_sign_in_links (expires_at);

ALTER TABLE public.auth_app_codes DROP CONSTRAINT auth_app_codes_kind_check;
ALTER TABLE public.auth_app_codes ADD CONSTRAINT auth_app_codes_kind_check
    CHECK (kind IN ('join', 'session', 'sign_in_link'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM public.auth_app_codes WHERE kind = 'sign_in_link';
ALTER TABLE public.auth_app_codes DROP CONSTRAINT auth_app_codes_kind_check;
ALTER TABLE public.auth_app_codes ADD CONSTRAINT auth_app_codes_kind_check
    CHECK (kind IN ('join', 'session'));
DROP TABLE IF EXISTS public.auth_sign_in_links;
-- +goose StatementEnd
