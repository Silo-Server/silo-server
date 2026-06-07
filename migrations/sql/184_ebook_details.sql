-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS public.ebook_details (
    content_id text PRIMARY KEY REFERENCES public.media_items(content_id) ON DELETE CASCADE,
    format text NOT NULL DEFAULT '',
    isbn text NOT NULL DEFAULT '',
    publisher text NOT NULL DEFAULT '',
    page_count integer NOT NULL DEFAULT 0,
    series_name text NOT NULL DEFAULT '',
    series_index text NOT NULL DEFAULT '',
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_ebook_details_series_name
    ON public.ebook_details (lower(series_name))
    WHERE series_name <> '';

CREATE INDEX IF NOT EXISTS idx_ebook_details_format
    ON public.ebook_details (format)
    WHERE format <> '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.idx_ebook_details_format;
DROP INDEX IF EXISTS public.idx_ebook_details_series_name;
DROP TABLE IF EXISTS public.ebook_details;
-- +goose StatementEnd
