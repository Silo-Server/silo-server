-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.push_deliveries ADD COLUMN profile_id text NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.push_deliveries DROP COLUMN profile_id;
-- +goose StatementEnd
