-- +goose Up
-- +goose StatementBegin
-- Items inside an abs_user_collections row. Composite PK rules out
-- duplicates. Both FKs cascade so a deleted collection or a deleted
-- media item silently drops the membership row.

CREATE TABLE IF NOT EXISTS public.abs_collection_items (
    collection_id   text NOT NULL REFERENCES public.abs_user_collections(id) ON DELETE CASCADE,
    library_item_id text NOT NULL REFERENCES public.media_items(content_id) ON DELETE CASCADE,
    added_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (collection_id, library_item_id)
);

CREATE INDEX IF NOT EXISTS abs_collection_items_library_item_idx
    ON public.abs_collection_items (library_item_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.abs_collection_items_library_item_idx;
DROP TABLE IF EXISTS public.abs_collection_items;
-- +goose StatementEnd
