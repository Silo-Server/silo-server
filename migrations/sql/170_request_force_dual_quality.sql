-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.request_settings
    ADD COLUMN IF NOT EXISTS force_dual_quality boolean NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.request_settings DROP COLUMN IF EXISTS force_dual_quality;
-- +goose StatementEnd
