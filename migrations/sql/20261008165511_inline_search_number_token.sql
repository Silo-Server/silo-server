-- +goose Up
-- +goose StatementBegin
-- Same output as migration 138's version, so title_normalized and the
-- expressions indexed over normalize_search_text stay valid without a rebuild.
-- A single-expression body lets PostgreSQL inline the function into its
-- callers instead of planning a CTE for every token of every title write. The
-- patterns avoid backslashes, whose meaning depends on standard_conforming_strings.
CREATE OR REPLACE FUNCTION public.normalize_search_number_token(token text)
RETURNS text
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
  SELECT CASE LOWER(COALESCE(token, ''))
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
        WHEN LOWER(COALESCE(token, '')) ~ '^[0-9]+(st|nd|rd|th)$'
          THEN REGEXP_REPLACE(LOWER(COALESCE(token, '')), '(st|nd|rd|th)$', '')
        ELSE LOWER(COALESCE(token, ''))
      END
  END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
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
-- +goose StatementEnd
