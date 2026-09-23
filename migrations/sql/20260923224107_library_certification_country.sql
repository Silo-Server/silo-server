-- +goose Up
ALTER TABLE media_folders ADD COLUMN certification_country text NOT NULL DEFAULT 'US'
    CHECK (certification_country IN ('US', 'AU'));

-- Country data is shared; the chosen country belongs to the library. Bind the
-- snapshot to its TMDB identity so reidentification cannot reuse old ratings.
CREATE TABLE media_item_certifications (
    content_id text PRIMARY KEY REFERENCES media_items(content_id) ON UPDATE CASCADE ON DELETE CASCADE,
    tmdb_id text NOT NULL,
    ratings jsonb NOT NULL DEFAULT '{}'::jsonb,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose StatementBegin
CREATE FUNCTION silo_normalize_certification(country text, rating text)
RETURNS text LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    WITH stripped AS (
      SELECT regexp_replace(UPPER(BTRIM(COALESCE(rating, ''))), '^' || country || '[-:]', '') AS value
    ), compact AS (
      SELECT CASE WHEN country = 'AU' THEN REPLACE(value, ' ', '') ELSE value END AS value FROM stripped
    )
    SELECT CASE WHEN country = 'AU' THEN CASE value
      WHEN 'M15+' THEN 'M' WHEN 'MA' THEN 'MA15+' WHEN 'MA15' THEN 'MA15+'
      WHEN 'R' THEN 'R18+' WHEN 'R18' THEN 'R18+' WHEN 'X' THEN 'X18+' WHEN 'X18' THEN 'X18+'
      ELSE value END ELSE value END FROM compact
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION silo_certification_rating(base text, country text, ratings jsonb, locked boolean)
RETURNS text LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    WITH normalised AS (
      SELECT UPPER(BTRIM(COALESCE(base, ''))) AS base,
             COALESCE(NULLIF(UPPER(BTRIM(country)), ''), 'US') AS country,
             silo_normalize_certification('AU', ratings->>'AU') AS au,
             silo_normalize_certification('US', ratings->>'US') AS us
    )
    SELECT CASE
      WHEN base LIKE 'AU-%' OR base LIKE 'AU:%' THEN
        COALESCE('AU-' || NULLIF(silo_normalize_certification('AU', base), ''), '')
      WHEN locked THEN silo_normalize_certification('US', base)
      WHEN country = 'AU' AND au <> '' THEN 'AU-' || au
      WHEN country = 'AU' THEN CASE COALESCE(NULLIF(us, ''), silo_normalize_certification('US', base))
        WHEN 'G' THEN 'AU-G' WHEN 'TV-G' THEN 'AU-G' WHEN 'TV-Y' THEN 'AU-G'
        WHEN 'PG' THEN 'AU-PG' WHEN 'TV-PG' THEN 'AU-PG' WHEN 'TV-Y7' THEN 'AU-PG' WHEN 'TV-Y7-FV' THEN 'AU-PG'
        WHEN 'PG-13' THEN 'AU-M' WHEN 'TV-14' THEN 'AU-M'
        WHEN 'R' THEN 'AU-R18+' WHEN 'NC-17' THEN 'AU-R18+' WHEN 'TV-MA' THEN 'AU-R18+'
        ELSE '' END
      ELSE COALESCE(NULLIF(base, ''), us) END FROM normalised
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION silo_certification_rating(text, text, jsonb, boolean);
DROP FUNCTION silo_normalize_certification(text, text);
DROP TABLE media_item_certifications;
ALTER TABLE media_folders DROP COLUMN certification_country;
