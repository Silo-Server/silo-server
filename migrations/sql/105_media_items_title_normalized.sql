-- +goose Up
-- +goose StatementBegin
-- OPERATOR NOTE: Adding a STORED generated column rewrites the entire
-- media_items table under an ACCESS EXCLUSIVE lock. On a 219K-row
-- production table this can take ~30s and blocks all reads and writes.
-- Smaller blast radius than migration 104 (which is on the 1.4M-row
-- media_files table), but still run during a maintenance window if possible.
--
-- Stored generated column matching the regex normalization currently
-- inlined in catalog/item_repo.go Search() ranking expressions.
-- Trigram-indexed so contiguous_title_match LIKE '%pattern%' uses an
-- index instead of seq-scanning the tsvector candidate set
-- (audit 2026-05-01 §3.12).

ALTER TABLE public.media_items
ADD COLUMN IF NOT EXISTS title_normalized text
  GENERATED ALWAYS AS (
    BTRIM(LOWER(REGEXP_REPLACE(COALESCE(title, ''), '[^[:alnum:]]+', ' ', 'g')))
  ) STORED;

CREATE INDEX IF NOT EXISTS idx_media_items_title_normalized_trgm
ON public.media_items USING gin (title_normalized public.gin_trgm_ops);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.idx_media_items_title_normalized_trgm;
ALTER TABLE public.media_items DROP COLUMN IF EXISTS title_normalized;
-- +goose StatementEnd
