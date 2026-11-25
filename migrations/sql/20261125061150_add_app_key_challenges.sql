-- +goose Up
-- +goose StatementBegin
-- App key challenges (docs/architecture/app-keys.md): every request an app
-- key signs for this server is on a challenge, which works once and for two
-- minutes. Only its SHA-256 is stored. Spending one deletes its row, so two
-- API processes cannot both accept it. A retention step deletes rows that
-- expired unspent.
CREATE TABLE public.auth_key_challenges (
    challenge_hash bytea PRIMARY KEY CHECK (octet_length(challenge_hash) = 32),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX auth_key_challenges_expires_at_idx ON public.auth_key_challenges (expires_at);

-- How a login session was opened. Some account changes (enrolling app keys,
-- replacing the person key) are refused from a session a device sign-in or
-- another app opened, and a session older than this column, with NULL here,
-- is refused like them.
ALTER TABLE public.auth_sessions
    ADD COLUMN opened_via text
        CONSTRAINT auth_sessions_opened_via_check
        CHECK (opened_via IN ('password', 'provider', 'device_login', 'app_key', 'impersonation'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.auth_sessions DROP COLUMN IF EXISTS opened_via;
DROP TABLE IF EXISTS public.auth_key_challenges;
-- +goose StatementEnd
