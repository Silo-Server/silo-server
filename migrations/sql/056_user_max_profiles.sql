-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.users
    ADD COLUMN max_profiles integer DEFAULT 0 NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.users
    DROP COLUMN IF EXISTS max_profiles;
-- +goose StatementEnd
