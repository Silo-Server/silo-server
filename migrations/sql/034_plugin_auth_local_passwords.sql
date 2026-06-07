-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.users
    ADD COLUMN local_password_login_enabled BOOLEAN NOT NULL DEFAULT TRUE;

UPDATE public.users
SET local_password_login_enabled = FALSE
WHERE id IN (
    SELECT DISTINCT user_id
    FROM public.plugin_auth_identities
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.users
    DROP COLUMN IF EXISTS local_password_login_enabled;
-- +goose StatementEnd
