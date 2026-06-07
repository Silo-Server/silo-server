-- +goose Up
-- +goose StatementBegin
-- OPERATOR NOTE: Adding STORED generated columns rewrites the entire
-- media_files table under an ACCESS EXCLUSIVE lock. On a 1.4M-row
-- production table this can take several minutes and blocks all reads
-- and writes (including playback session opens, scanning, and metadata
-- refresh). Run during a planned maintenance window.
--
-- Approximate rewrite cost: O(rows * (audio_track_count + subtitle_track_count)),
-- dominated by jsonb_array_elements over the JSONB columns. Expect
-- ~1-3 minutes on a 1.4M-row table with ~3 audio tracks each.
--
-- Migration is reversible via the Goose Down section.

-- Generated stored arrays of audio/subtitle language codes pulled from
-- the JSONB tracks columns. Replaces per-row jsonb_array_elements unnest
-- in audio_language / subtitle_language filters (audit 2026-05-01 §2.5b).
--
-- IMPLEMENTATION NOTE: Postgres prohibits subqueries (including ARRAY(SELECT ...))
-- inside generated-column expressions. Wrap the extraction in an IMMUTABLE
-- SQL function so the generated-column expression is just a function call.
--
-- IMMUTABILITY CAVEAT: jsonb_array_elements is documented STABLE, not
-- IMMUTABLE. Marking this wrapper IMMUTABLE is a deliberate (and standard)
-- workaround required for STORED generated columns. It is safe in practice:
-- given the same input jsonb, this function is fully deterministic and has
-- no dependency on session/transaction state — the STABLE marker on
-- jsonb_array_elements exists for catalog-version concerns that don't apply
-- to text-extraction over a literal jsonb argument. Don't use this function
-- in expression indexes that need cross-version stability guarantees.

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
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.idx_media_files_subtitle_lang_gin;
DROP INDEX IF EXISTS public.idx_media_files_audio_lang_gin;
ALTER TABLE public.media_files DROP COLUMN IF EXISTS subtitle_language_codes;
ALTER TABLE public.media_files DROP COLUMN IF EXISTS audio_language_codes;
DROP FUNCTION IF EXISTS public.jsonb_track_language_codes(jsonb);
-- +goose StatementEnd
