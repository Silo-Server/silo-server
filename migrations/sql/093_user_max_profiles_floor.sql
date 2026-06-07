-- +goose Up
-- +goose StatementBegin
UPDATE public.users
SET max_profiles = 5
WHERE max_profiles < 1;

ALTER TABLE public.users
    ALTER COLUMN max_profiles SET DEFAULT 5,
    ADD CONSTRAINT users_max_profiles_min_check CHECK (max_profiles >= 1);

UPDATE public.server_settings
SET value = '5'
WHERE key = 'defaults.max_profiles'
  AND (value IS NULL OR value = '' OR value = '0');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.users
    DROP CONSTRAINT IF EXISTS users_max_profiles_min_check,
    ALTER COLUMN max_profiles SET DEFAULT 0;
-- +goose StatementEnd
