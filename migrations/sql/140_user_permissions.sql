-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.users
    ADD COLUMN IF NOT EXISTS permissions text[] NOT NULL DEFAULT '{}'::text[];

UPDATE public.users
SET permissions = '{}'::text[]
WHERE permissions IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.users
    DROP COLUMN IF EXISTS permissions;
-- +goose StatementEnd
