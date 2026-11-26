-- +goose Up
-- damaged_failures counts how many of a row's consecutive failures said the
-- file's data is broken. Such a verdict makes the row unusable only after
-- several in a row, because storage that serves bad bytes during an outage
-- looks the same to ffmpeg as a damaged file. Servers from before this column
-- reset failure_count without it, so the count is read capped at
-- failure_count.
ALTER TABLE public.media_file_trickplay
    ADD COLUMN damaged_failures integer NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE public.media_file_trickplay DROP COLUMN IF EXISTS damaged_failures;
