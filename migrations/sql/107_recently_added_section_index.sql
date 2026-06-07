-- +goose Up
-- +goose StatementBegin
CREATE INDEX IF NOT EXISTS idx_item_libraries_folder_seen_content
ON public.media_item_libraries USING btree (media_folder_id, first_seen_at DESC, content_id ASC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.idx_item_libraries_folder_seen_content;
-- +goose StatementEnd
