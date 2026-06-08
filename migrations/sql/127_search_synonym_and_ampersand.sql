-- +goose Up
-- +goose StatementBegin
-- OPERATOR NOTE: This migration drops and re-adds the title_normalized
-- STORED generated column on media_items, which rewrites the entire table
-- under an ACCESS EXCLUSIVE lock. On a 219K-row production table this can
-- take ~30s and blocks all reads and writes. Same blast radius as
-- migration 105. Run during a maintenance window if possible.
--
-- It also rebuilds idx_media_items_search_title_fields, so search queries
-- will fall back to seq scans against the title tsvector arm for the
-- duration of the rebuild.
--
-- Goal: make "&" and the word "and" interchangeable in FTS search so
-- "Law & Order" and "Law and Order" return the same items. The existing
-- normalization replaced "&" with a space but kept "and" as a token,
-- producing asymmetric matches.

-- Single source of truth for search text normalization. Used by:
--   * media_items.title_normalized generated column
--   * idx_media_items_search_title_fields GIN expression
--   * Inline original_title / sort_title normalization in buildSearchSQL
--   * websearch_to_tsquery() argument wrapping
--
-- Strips non-alphanumeric chars to spaces, lowercases, and drops runs of
-- standalone "and" tokens. Punctuation-equivalent symbols like "&" collapse
-- to a space in the first regex pass, so they vanish too. Returns '' when
-- input is NULL.
--
-- NOTE: This assumes a UTF-8 lc_ctype locale so [:alnum:] includes non-ASCII
-- letters. The Go-side mirror normalizeTitleForComparison uses Unicode-aware
-- IsLetter/IsDigit; the two diverge on C/POSIX-locale databases.
CREATE OR REPLACE FUNCTION public.normalize_search_text(input text)
RETURNS text
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT BTRIM(REGEXP_REPLACE(
    ' ' || BTRIM(LOWER(REGEXP_REPLACE(COALESCE(input, ''), '[^[:alnum:]]+', ' ', 'g'))) || ' ',
    ' (and )+',
    ' ',
    'g'
  ));
$$;

-- Drop indexes that reference the old (un-wrapped) expressions or the
-- previous generated column. They are recreated below.
DROP INDEX IF EXISTS public.idx_media_items_search_title_fields;
DROP INDEX IF EXISTS public.idx_media_items_title_normalized_trgm;

-- The legacy idx_media_items_search (english config, title || overview)
-- was never used by the current query path; clean it up here.
DROP INDEX IF EXISTS public.idx_media_items_search;

ALTER TABLE public.media_items DROP COLUMN IF EXISTS title_normalized;
ALTER TABLE public.media_items
  ADD COLUMN title_normalized text
  GENERATED ALWAYS AS (public.normalize_search_text(title)) STORED;

CREATE INDEX IF NOT EXISTS idx_media_items_title_normalized_trgm
  ON public.media_items USING gin (title_normalized public.gin_trgm_ops);

CREATE INDEX IF NOT EXISTS idx_media_items_search_title_fields
  ON public.media_items USING gin ((
    setweight(to_tsvector('simple', public.normalize_search_text(COALESCE(title, ''))), 'A') ||
    setweight(to_tsvector('simple', public.normalize_search_text(COALESCE(original_title, ''))), 'A') ||
    setweight(to_tsvector('simple', public.normalize_search_text(COALESCE(sort_title, ''))), 'B')
  ));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Restore the pre-127 normalization: collapse non-alnum to spaces and
-- lowercase, but DO NOT strip standalone "and" tokens. "&" and "and" will
-- once again be inequivalent in title matching.
--
-- The legacy idx_media_items_search (english combined title+overview) is
-- intentionally NOT restored: it was unused by the post-migration-105 query
-- path, and rolling back to a known-dead index just imposes a GIN rebuild
-- cost for no benefit.

DROP INDEX IF EXISTS public.idx_media_items_search_title_fields;
DROP INDEX IF EXISTS public.idx_media_items_title_normalized_trgm;

ALTER TABLE public.media_items DROP COLUMN IF EXISTS title_normalized;
ALTER TABLE public.media_items
  ADD COLUMN title_normalized text
  GENERATED ALWAYS AS (
    BTRIM(LOWER(REGEXP_REPLACE(COALESCE(title, ''), '[^[:alnum:]]+', ' ', 'g')))
  ) STORED;

CREATE INDEX IF NOT EXISTS idx_media_items_title_normalized_trgm
  ON public.media_items USING gin (title_normalized public.gin_trgm_ops);

CREATE INDEX IF NOT EXISTS idx_media_items_search_title_fields
  ON public.media_items USING gin ((
    setweight(to_tsvector('simple', COALESCE(title, '')), 'A') ||
    setweight(to_tsvector('simple', COALESCE(original_title, '')), 'A') ||
    setweight(to_tsvector('simple', COALESCE(sort_title, '')), 'B')
  ));

DROP FUNCTION IF EXISTS public.normalize_search_text(text);
-- +goose StatementEnd
