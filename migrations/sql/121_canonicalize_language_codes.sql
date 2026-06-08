-- +goose Up
-- +goose StatementBegin
-- Canonicalize language and country codes across the catalog. Different
-- ingest paths historically wrote ISO 639-1 ("en"), ISO 639-2/B ("eng"),
-- and case variants verbatim, accumulating duplicates of the same logical
-- value. This migration collapses them to canonical lowercase ISO 639-1
-- (or the 3-letter form for languages without a 2-letter equivalent) and
-- ISO 3166-1 alpha-2 uppercase for countries.
--
-- OPERATOR NOTE: rewrites the entire media_files table again under an
-- ACCESS EXCLUSIVE lock when the audio/subtitle generated columns are
-- replaced. See migration 104 for the cost profile (~1-3 minutes on a
-- 1.4M-row table). The media_items updates are cheap by comparison.
-- Run during a planned maintenance window.

-- Hand-rolled ISO 639-2/B + 639-2/T -> 639-1 mapping. Mirrors the Go
-- helper in internal/lang. Unrecognized codes are returned as
-- lower(trim(code)) so we never silently drop data.
CREATE OR REPLACE FUNCTION public.canonical_language_code(code text)
RETURNS text
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
    SELECT CASE LOWER(BTRIM(code))
        WHEN 'eng' THEN 'en' WHEN 'jpn' THEN 'ja' WHEN 'fra' THEN 'fr' WHEN 'fre' THEN 'fr'
        WHEN 'deu' THEN 'de' WHEN 'ger' THEN 'de' WHEN 'spa' THEN 'es' WHEN 'ita' THEN 'it'
        WHEN 'por' THEN 'pt' WHEN 'rus' THEN 'ru' WHEN 'zho' THEN 'zh' WHEN 'chi' THEN 'zh'
        WHEN 'kor' THEN 'ko' WHEN 'ara' THEN 'ar' WHEN 'nld' THEN 'nl' WHEN 'dut' THEN 'nl'
        WHEN 'pol' THEN 'pl' WHEN 'swe' THEN 'sv' WHEN 'dan' THEN 'da'
        WHEN 'fin' THEN 'fi' WHEN 'tur' THEN 'tr' WHEN 'ces' THEN 'cs' WHEN 'cze' THEN 'cs'
        WHEN 'slk' THEN 'sk' WHEN 'slo' THEN 'sk' WHEN 'hun' THEN 'hu' WHEN 'heb' THEN 'he'
        WHEN 'tha' THEN 'th' WHEN 'ind' THEN 'id' WHEN 'msa' THEN 'ms' WHEN 'may' THEN 'ms'
        WHEN 'vie' THEN 'vi' WHEN 'ukr' THEN 'uk' WHEN 'ron' THEN 'ro' WHEN 'rum' THEN 'ro'
        WHEN 'bul' THEN 'bg' WHEN 'hrv' THEN 'hr' WHEN 'srp' THEN 'sr' WHEN 'slv' THEN 'sl'
        WHEN 'ell' THEN 'el' WHEN 'gre' THEN 'el' WHEN 'hin' THEN 'hi' WHEN 'tam' THEN 'ta'
        WHEN 'tel' THEN 'te' WHEN 'ben' THEN 'bn' WHEN 'fas' THEN 'fa' WHEN 'per' THEN 'fa'
        WHEN 'cat' THEN 'ca' WHEN 'lit' THEN 'lt' WHEN 'lav' THEN 'lv' WHEN 'est' THEN 'et'
        WHEN 'isl' THEN 'is' WHEN 'ice' THEN 'is' WHEN 'mlt' THEN 'mt' WHEN 'gle' THEN 'ga'
        WHEN 'cym' THEN 'cy' WHEN 'wel' THEN 'cy' WHEN 'mya' THEN 'my' WHEN 'bur' THEN 'my'
        WHEN 'khm' THEN 'km' WHEN 'lao' THEN 'lo' WHEN 'sin' THEN 'si' WHEN 'mar' THEN 'mr'
        WHEN 'pan' THEN 'pa' WHEN 'guj' THEN 'gu' WHEN 'kan' THEN 'kn' WHEN 'mal' THEN 'ml'
        WHEN 'urd' THEN 'ur' WHEN 'nep' THEN 'ne' WHEN 'aze' THEN 'az' WHEN 'kat' THEN 'ka'
        WHEN 'geo' THEN 'ka' WHEN 'hye' THEN 'hy' WHEN 'arm' THEN 'hy' WHEN 'kaz' THEN 'kk'
        WHEN 'uzb' THEN 'uz' WHEN 'mon' THEN 'mn' WHEN 'mlg' THEN 'mg' WHEN 'swa' THEN 'sw'
        WHEN 'zul' THEN 'zu' WHEN 'afr' THEN 'af' WHEN 'amh' THEN 'am' WHEN 'lat' THEN 'la'
        WHEN 'epo' THEN 'eo' WHEN 'baq' THEN 'eu' WHEN 'eus' THEN 'eu' WHEN 'glg' THEN 'gl'
        WHEN 'nor' THEN 'no' WHEN 'nob' THEN 'nb' WHEN 'nno' THEN 'nn'
        WHEN '' THEN ''
        ELSE LOWER(BTRIM(code))
    END
$$;

-- Backfill media_items.original_language. Cheap UPDATE, partial index on
-- the column auto-updates; only changed rows are touched.
UPDATE public.media_items
SET original_language = public.canonical_language_code(original_language)
WHERE original_language <> public.canonical_language_code(original_language);

-- Backfill media_items.countries[]. Uppercase + trim + drop blanks.
-- INTENTIONAL DIVERGENCE FROM Go's lang.CanonicalCountry: that helper
-- uses x/text/language.ParseRegion to convert alpha-3 ("USA") to
-- alpha-2 ("US"), but this migration does not. Reason: TMDB and TVDB
-- both emit alpha-2 already, so any alpha-3 codes in existing rows are
-- rare edge cases that will normalize on the next metadata refresh
-- (which routes through lang.CanonicalCountry). Adding an alpha-3
-- table to the SQL would duplicate ~250 entries for negligible benefit.
-- The catalog code path is the single source of truth going forward.
UPDATE public.media_items
SET countries = ARRAY(SELECT UPPER(BTRIM(c)) FROM unnest(countries) AS c WHERE BTRIM(c) <> '')
WHERE countries IS NOT NULL
  AND countries <> ARRAY(SELECT UPPER(BTRIM(c)) FROM unnest(countries) AS c WHERE BTRIM(c) <> '');

-- Replace the JSONB track-language extractor with a canonicalizing version.
-- The two STORED generated columns must be dropped and re-added to force
-- recomputation; Postgres does not auto-recompute STORED columns when the
-- referenced function changes.
CREATE OR REPLACE FUNCTION public.jsonb_track_language_codes(tracks jsonb)
RETURNS text[]
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
    SELECT array_agg(public.canonical_language_code(elem->>'language'))
    FROM jsonb_array_elements(COALESCE(tracks, '[]'::jsonb)) AS elem
    WHERE (elem->>'language') IS NOT NULL AND (elem->>'language') <> ''
$$;

DROP INDEX IF EXISTS idx_media_files_audio_lang_gin;
DROP INDEX IF EXISTS idx_media_files_subtitle_lang_gin;

ALTER TABLE public.media_files DROP COLUMN IF EXISTS audio_language_codes;
ALTER TABLE public.media_files DROP COLUMN IF EXISTS subtitle_language_codes;

ALTER TABLE public.media_files
ADD COLUMN IF NOT EXISTS audio_language_codes text[]
  GENERATED ALWAYS AS (public.jsonb_track_language_codes(audio_tracks)) STORED;

ALTER TABLE public.media_files
ADD COLUMN IF NOT EXISTS subtitle_language_codes text[]
  GENERATED ALWAYS AS (public.jsonb_track_language_codes(subtitle_tracks)) STORED;

CREATE INDEX IF NOT EXISTS idx_media_files_audio_lang_gin
ON public.media_files USING gin (audio_language_codes);

CREATE INDEX IF NOT EXISTS idx_media_files_subtitle_lang_gin
ON public.media_files USING gin (subtitle_language_codes);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Rollback canonicalization. Original code values were lossy on the way
-- down (multiple raw forms collapsed to one), so the data rewrite cannot
-- be reversed. This rollback restores the previous LOWER-only function
-- and re-creates the STORED columns; downstream UI will see whatever
-- canonical values the up migration left behind.

DROP INDEX IF EXISTS idx_media_files_audio_lang_gin;
DROP INDEX IF EXISTS idx_media_files_subtitle_lang_gin;

ALTER TABLE public.media_files DROP COLUMN IF EXISTS audio_language_codes;
ALTER TABLE public.media_files DROP COLUMN IF EXISTS subtitle_language_codes;

CREATE OR REPLACE FUNCTION public.jsonb_track_language_codes(tracks jsonb)
RETURNS text[]
LANGUAGE sql
IMMUTABLE
PARALLEL SAFE
AS $$
    SELECT array_agg(LOWER(elem->>'language'))
    FROM jsonb_array_elements(COALESCE(tracks, '[]'::jsonb)) AS elem
    WHERE (elem->>'language') IS NOT NULL AND (elem->>'language') <> ''
$$;

ALTER TABLE public.media_files
ADD COLUMN IF NOT EXISTS audio_language_codes text[]
  GENERATED ALWAYS AS (public.jsonb_track_language_codes(audio_tracks)) STORED;

ALTER TABLE public.media_files
ADD COLUMN IF NOT EXISTS subtitle_language_codes text[]
  GENERATED ALWAYS AS (public.jsonb_track_language_codes(subtitle_tracks)) STORED;

CREATE INDEX IF NOT EXISTS idx_media_files_audio_lang_gin
ON public.media_files USING gin (audio_language_codes);

CREATE INDEX IF NOT EXISTS idx_media_files_subtitle_lang_gin
ON public.media_files USING gin (subtitle_language_codes);

DROP FUNCTION IF EXISTS public.canonical_language_code(text);
-- +goose StatementEnd
