-- +goose Up
-- +goose StatementBegin
-- The access group is now the sole source of default policy for new users.
-- A fresh user row means "no restriction at the user layer" (0 = unlimited),
-- so the seeded Default Group's ceilings apply cleanly instead of fighting
-- the old per-user 6-stream / 2-transcode defaults. Existing rows keep their
-- stored values: users created under the old regime are not silently uncapped.
ALTER TABLE public.users ALTER COLUMN max_streams SET DEFAULT 0;
ALTER TABLE public.users ALTER COLUMN max_transcodes SET DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.users ALTER COLUMN max_streams SET DEFAULT 6;
ALTER TABLE public.users ALTER COLUMN max_transcodes SET DEFAULT 2;
-- +goose StatementEnd
