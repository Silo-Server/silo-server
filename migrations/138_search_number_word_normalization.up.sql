-- OPERATOR NOTE: This migration drops and re-adds the title_normalized
-- STORED generated column on media_items, which rewrites the entire table
-- under an ACCESS EXCLUSIVE lock. It also rebuilds the title search GIN
-- indexes, so search queries will fall back to slower plans while the
-- indexes are being rebuilt. Run during a maintenance window on large
-- libraries.
--
-- Goal: make common title numbers interchangeable between word and digit
-- forms in FTS search, so "Dune: Part Two" and "Dune Part 2" match the same
-- title-side tokens.

DROP INDEX IF EXISTS public.idx_media_items_search_title_fields;
DROP INDEX IF EXISTS public.idx_media_items_title_normalized_trgm;

ALTER TABLE public.media_items DROP COLUMN IF EXISTS title_normalized;

CREATE OR REPLACE FUNCTION public.normalize_search_number_token(token text)
RETURNS text
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  WITH normalized AS (
    SELECT LOWER(COALESCE(token, '')) AS value
  )
  SELECT CASE value
    WHEN 'zero' THEN '0'
    WHEN 'zeroth' THEN '0'
    WHEN 'one' THEN '1'
    WHEN 'first' THEN '1'
    WHEN 'two' THEN '2'
    WHEN 'second' THEN '2'
    WHEN 'three' THEN '3'
    WHEN 'third' THEN '3'
    WHEN 'four' THEN '4'
    WHEN 'fourth' THEN '4'
    WHEN 'five' THEN '5'
    WHEN 'fifth' THEN '5'
    WHEN 'six' THEN '6'
    WHEN 'sixth' THEN '6'
    WHEN 'seven' THEN '7'
    WHEN 'seventh' THEN '7'
    WHEN 'eight' THEN '8'
    WHEN 'eighth' THEN '8'
    WHEN 'nine' THEN '9'
    WHEN 'ninth' THEN '9'
    WHEN 'ten' THEN '10'
    WHEN 'tenth' THEN '10'
    WHEN 'eleven' THEN '11'
    WHEN 'eleventh' THEN '11'
    WHEN 'twelve' THEN '12'
    WHEN 'twelfth' THEN '12'
    WHEN 'thirteen' THEN '13'
    WHEN 'thirteenth' THEN '13'
    WHEN 'fourteen' THEN '14'
    WHEN 'fourteenth' THEN '14'
    WHEN 'fifteen' THEN '15'
    WHEN 'fifteenth' THEN '15'
    WHEN 'sixteen' THEN '16'
    WHEN 'sixteenth' THEN '16'
    WHEN 'seventeen' THEN '17'
    WHEN 'seventeenth' THEN '17'
    WHEN 'eighteen' THEN '18'
    WHEN 'eighteenth' THEN '18'
    WHEN 'nineteen' THEN '19'
    WHEN 'nineteenth' THEN '19'
    WHEN 'twenty' THEN '20'
    WHEN 'twentieth' THEN '20'
    ELSE
      CASE
        WHEN value ~ '^[0-9]+(st|nd|rd|th)$' THEN REGEXP_REPLACE(value, '(st|nd|rd|th)$', '')
        ELSE value
      END
  END
  FROM normalized;
$$;

-- Single source of truth for search text normalization. Used by:
--   * media_items.title_normalized generated column
--   * idx_media_items_search_title_fields GIN expression
--   * Inline original_title / sort_title normalization in buildSearchSQL
--   * websearch_to_tsquery() argument wrapping
--
-- Strips non-alphanumeric chars to spaces, lowercases, drops standalone
-- "and" tokens, normalizes common number words / ordinals to digit tokens,
-- and rejoins tokens in original order. Returns '' when input is NULL.
CREATE OR REPLACE FUNCTION public.normalize_search_text(input text)
RETURNS text
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT COALESCE(
    STRING_AGG(public.normalize_search_number_token(token), ' ' ORDER BY ord),
    ''
  )
  FROM REGEXP_SPLIT_TO_TABLE(
    BTRIM(LOWER(REGEXP_REPLACE(COALESCE(input, ''), '[^[:alnum:]]+', ' ', 'g'))),
    '[[:space:]]+'
  ) WITH ORDINALITY AS tokens(token, ord)
  WHERE token <> '' AND token <> 'and';
$$;

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
