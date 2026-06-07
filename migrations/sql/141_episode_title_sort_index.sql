-- +goose Up
-- +goose StatementBegin
-- Episode library title browse. Matches episodeCatalogSelectBody's sort_key
-- expression so PostgreSQL can satisfy ORDER BY title from the episodes index.
CREATE INDEX IF NOT EXISTS idx_episodes_sort_key_content
ON public.episodes USING btree (
    LOWER(COALESCE(NULLIF(BTRIM(title), ''), 'Episode ' || episode_number::text)),
    content_id
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.idx_episodes_sort_key_content;
-- +goose StatementEnd
