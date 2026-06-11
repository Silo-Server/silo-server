-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.push_deliveries ADD COLUMN profile_id text NULL;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE public.push_deliveries ADD CONSTRAINT push_deliveries_profile_fkey FOREIGN KEY (user_id, profile_id) REFERENCES public.user_profiles(user_id, id) ON DELETE CASCADE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.push_deliveries DROP CONSTRAINT IF EXISTS push_deliveries_profile_fkey;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE public.push_deliveries DROP COLUMN profile_id;
-- +goose StatementEnd
