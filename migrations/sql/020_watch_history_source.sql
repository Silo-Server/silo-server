-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.user_watch_history
    ADD COLUMN IF NOT EXISTS source text;

UPDATE public.user_watch_history
SET source = 'legacy'
WHERE source IS NULL;

ALTER TABLE public.user_watch_history
    ALTER COLUMN source SET DEFAULT 'legacy';

ALTER TABLE public.user_watch_history
    ALTER COLUMN source SET NOT NULL;
-- +goose StatementEnd
