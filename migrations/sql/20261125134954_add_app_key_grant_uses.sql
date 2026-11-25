-- +goose Up
-- +goose StatementBegin
-- Grants another app redeemed (docs/architecture/app-keys.md, "Grants"): a
-- grant works once, so its ID is recorded in the transaction that enrolls the
-- app's key, and a second redemption on any API process finds the row. A row
-- outlives its grant's exp by the retention step's margin, then the
-- retention step deletes it: an expired grant is refused by its exp. The
-- signer chooses a grant's ID, so the record is per account: one account's
-- grant can never use up another's.
CREATE TABLE public.auth_grant_uses (
    user_id integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    grant_id bytea NOT NULL CHECK (octet_length(grant_id) = 16),
    used_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (user_id, grant_id)
);
CREATE INDEX auth_grant_uses_expires_at_idx ON public.auth_grant_uses (expires_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.auth_grant_uses;
-- +goose StatementEnd
