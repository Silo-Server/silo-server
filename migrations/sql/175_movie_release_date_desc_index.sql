-- +goose Up
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_media_items_movie_release_date_desc_content
ON public.media_items USING btree (release_date DESC NULLS LAST, content_id ASC)
WHERE type = 'movie';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.idx_media_items_movie_release_date_desc_content;
-- +goose StatementEnd
