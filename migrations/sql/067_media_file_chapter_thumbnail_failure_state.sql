-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.media_files
    ADD COLUMN chapter_thumbnail_retry_after timestamptz,
    ADD COLUMN chapter_thumbnail_failure_count integer NOT NULL DEFAULT 0,
    ADD COLUMN chapter_thumbnail_last_error text;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.media_files
    DROP COLUMN IF EXISTS chapter_thumbnail_last_error,
    DROP COLUMN IF EXISTS chapter_thumbnail_failure_count,
    DROP COLUMN IF EXISTS chapter_thumbnail_retry_after;
-- +goose StatementEnd
