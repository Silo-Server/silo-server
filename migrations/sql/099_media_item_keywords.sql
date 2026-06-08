-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.media_items
ADD COLUMN IF NOT EXISTS keywords TEXT[] NOT NULL DEFAULT '{}'::TEXT[];
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.media_items
DROP COLUMN IF EXISTS keywords;
-- +goose StatementEnd
