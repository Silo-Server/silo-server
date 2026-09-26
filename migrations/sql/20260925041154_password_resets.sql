-- +goose Up
-- +goose StatementBegin
-- An administrator can make a password temporary: the account must choose a
-- new one before its sessions can do anything else. NOT NULL DEFAULT false is
-- a catalog-only change that keeps every existing account on today's behavior.
ALTER TABLE public.users
    ADD COLUMN password_change_required boolean NOT NULL DEFAULT false;

-- Password reset links: a single-use, time-limited capability to replace one
-- account's local password. Keyed by account, so an account has at most one
-- link and issuing a new one replaces the old. Completing a reset deletes the
-- row, which is what makes a link single-use.
CREATE TABLE public.password_reset_tokens (
    user_id              integer PRIMARY KEY REFERENCES public.users(id) ON DELETE CASCADE,
    -- SHA-256 hex of the link token. The raw token exists only in the link;
    -- a database dump yields no usable links.
    token_hash           text NOT NULL UNIQUE,
    -- SHA-256 hex of users.password_hash when the link was issued. A link
    -- only works while the account still has that password, so any other
    -- password change retires it without touching this table.
    password_fingerprint text NOT NULL,
    issued_by            integer REFERENCES public.users(id) ON DELETE SET NULL,
    expires_at           timestamptz NOT NULL,
    created_at           timestamptz NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE public.password_reset_tokens;
ALTER TABLE public.users DROP COLUMN password_change_required;
-- +goose StatementEnd
