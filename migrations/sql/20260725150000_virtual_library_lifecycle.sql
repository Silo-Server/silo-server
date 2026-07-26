-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.media_items
    ADD COLUMN IF NOT EXISTS virtual_owner_installation_id bigint,
    ADD COLUMN IF NOT EXISTS virtual_source text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS virtual_last_seen_at timestamp with time zone;

CREATE INDEX IF NOT EXISTS idx_media_items_virtual_owner
    ON public.media_items (virtual_owner_installation_id, virtual_source)
    WHERE virtual_owner_installation_id IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.idx_media_items_virtual_owner;
ALTER TABLE public.media_items
    DROP COLUMN IF EXISTS virtual_last_seen_at,
    DROP COLUMN IF EXISTS virtual_source,
    DROP COLUMN IF EXISTS virtual_owner_installation_id;
-- +goose StatementEnd
