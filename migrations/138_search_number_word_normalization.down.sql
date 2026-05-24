-- Restore the pre-138 normalization: collapse non-alnum to spaces, lowercase,
-- and strip standalone "and" tokens, but do not normalize number words or
-- ordinal digit suffixes.

DROP INDEX IF EXISTS public.idx_media_items_search_title_fields;
DROP INDEX IF EXISTS public.idx_media_items_title_normalized_trgm;

ALTER TABLE public.media_items DROP COLUMN IF EXISTS title_normalized;

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

DROP FUNCTION IF EXISTS public.normalize_search_number_token(text);
