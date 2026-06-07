-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.media_items
    ADD COLUMN IF NOT EXISTS release_date date;

UPDATE public.media_items
SET release_date = NULLIF(first_air_date, '')::date
WHERE release_date IS NULL
  AND first_air_date IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.media_items
    DROP COLUMN IF EXISTS release_date;
-- +goose StatementEnd
