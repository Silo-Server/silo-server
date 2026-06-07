-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.media_files
    ADD COLUMN chapters jsonb;

ALTER TABLE public.media_folders
    ADD COLUMN chapter_thumbnails_enabled boolean NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.media_folders
    DROP COLUMN IF EXISTS chapter_thumbnails_enabled;

ALTER TABLE public.media_files
    DROP COLUMN IF EXISTS chapters;
-- +goose StatementEnd
