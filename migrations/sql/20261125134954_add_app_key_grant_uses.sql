-- +goose Up
-- +goose StatementBegin
-- Grants another app redeemed (docs/architecture/app-keys.md, "Grants"): a
-- grant works once, so its ID is recorded in the transaction that enrolls the
-- app's key, and a second redemption on any API process finds the row. A row
-- outlives its grant's exp by the retention step's margin, then the
-- retention step deletes it: an expired grant is refused by its exp.
CREATE TABLE public.auth_grant_uses (
    grant_id bytea PRIMARY KEY CHECK (octet_length(grant_id) = 16),
    used_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX auth_grant_uses_expires_at_idx ON public.auth_grant_uses (expires_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.auth_grant_uses;
-- +goose StatementEnd
