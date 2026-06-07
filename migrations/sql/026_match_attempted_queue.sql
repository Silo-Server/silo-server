-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.media_files
    ADD COLUMN match_attempted_at timestamp with time zone;

CREATE INDEX idx_media_files_unmatched_match_queue
    ON public.media_files USING btree (match_attempted_at ASC NULLS FIRST, id ASC)
    WHERE (((content_id IS NULL) OR (content_id = ''::text)) AND (missing_since IS NULL));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.idx_media_files_unmatched_match_queue;

ALTER TABLE public.media_files
    DROP COLUMN IF EXISTS match_attempted_at;
-- +goose StatementEnd
