-- +goose NO TRANSACTION

-- +goose Up
-- Minimum viewer age is the universal maturity axis: every national system is
-- age based, and the US one only hides the age behind letters. Storing the age
-- makes BBFC 15, FSK 16, PG-13 and Kijkwijzer 12 comparable, and lets ceiling
-- filters read an integer instead of matching uppercase US literals.
--
-- content_rating itself is unchanged and stays the string clients display.
--
-- The CASE below is a one-time backfill and is frozen in this migration
-- forever. It deliberately inlines its vocabulary instead of calling a helper
-- function: later code must never be able to rewrite applied history. Ongoing
-- normalization of new and edited ratings lives in Go.
--
-- The file runs without a wrapping transaction so that a large catalog keeps
-- serving reads while it applies: the backfill takes row locks rather than
-- holding media_items exclusively, and the indexes build concurrently. Every
-- statement is safe to re-run after a partial failure.
ALTER TABLE public.media_items
    ADD COLUMN IF NOT EXISTS content_rating_age smallint;

ALTER TABLE public.episode_catalog_entries
    ADD COLUMN IF NOT EXISTS content_rating_age smallint;

-- Backfill media_items from the rating string already stored there.
--
-- Everything below is inlined literals on purpose. The ladder it encodes is the
-- same vocabulary internal/access.Normalize resolves at runtime, but this copy
-- is frozen: applied history must never change because later Go code did, so
-- this statement calls no helper function a future migration or release could
-- redefine.
--
-- Resolution mirrors Normalize step for step:
--   1. strip a leading two or three letter country code ("DE:16", "gb:15");
--   2. drop a "Rated " word prefix, then fold the remainder to a lookup token
--      by uppercasing it and dropping every character that is not a letter, a
--      digit or "+" ("PG-13" and "pg 13" both become "PG13");
--   3. when the whole string is not a rating, retry with just the first word
--      after "Rated" ("Rated R for strong language" -> "R");
--   4. read the age off the ladder, falling back to a bare age ("16", "16+").
--
-- A title with no rating keeps a NULL age: an empty string, or an unrated
-- marker (NR, UR, Unrated, Not Rated, N/A, None, Unknown). The server setting
-- access.unrated_content decides whether ceilinged profiles see those. Any
-- other text no ladder recognizes stores 99 (access.UnrecognizedRatingAge), so
-- it stays hidden from every ceilinged profile whatever that setting says.
--
-- It runs before the series trigger below starts watching content_rating_age,
-- so writing the age does not refresh every episode of every series one by
-- one; the bulk episode update further down copies the ages instead.
--
-- The BTRIM character sets below are spelled out because one-argument BTRIM
-- strips U+0020 only, while Go trims every ASCII whitespace character plus NEL
-- and NBSP. A rating that arrives with a leading tab (providers store the
-- string verbatim) has to resolve to the same age here as it does in Go.
WITH raw AS (
    SELECT
        mi.content_id,
        BTRIM(mi.content_rating, E' \t\n\r\v\f\u0085\u00A0') AS value
    FROM public.media_items mi
    WHERE mi.content_rating IS NOT NULL
),
split AS (
    SELECT
        r.content_id,
        CASE
            WHEN r.value ~ '^[A-Za-z]{2,3}[[:space:]]*:'
                THEN UPPER(SUBSTRING(r.value FROM '^([A-Za-z]{2,3})'))
        END AS code,
        UPPER(BTRIM(REGEXP_REPLACE(r.value, '^[A-Za-z]{2,3}[[:space:]]*:', ''), E' \t\n\r\v\f\u0085\u00A0')) AS rest
    FROM raw r
),
rated AS (
    SELECT
        s.content_id,
        s.code,
        s.rest,
        CASE
            WHEN s.rest ~ '^RATED[^A-Z]' THEN BTRIM(SUBSTRING(s.rest FROM 6), E' \t\n\r\v\f\u0085\u00A0')
            ELSE s.rest
        END AS body,
        s.rest ~ '^RATED[^A-Z]' AS was_rated
    FROM split s
),
candidates AS (
    SELECT
        t.content_id,
        t.code,
        c.ord,
        REGEXP_REPLACE(c.candidate, '[^A-Z0-9+]', '', 'g') AS token
    FROM rated t
    CROSS JOIN LATERAL (
        VALUES
            -- The whole string, which is what almost every provider writes.
            (1, t.body),
            -- "Rated R for strong language": the first word after "Rated",
            -- tried only when the whole string resolved to nothing.
            (2, CASE
                    WHEN t.was_rated AND t.body ~ '[[:space:]]'
                        THEN SUBSTRING(t.body FROM '^[^[:space:]]+')
                END)
    ) AS c(ord, candidate)
    WHERE NULLIF(BTRIM(COALESCE(c.candidate, '')), '') IS NOT NULL
),
scored AS (
    SELECT
        c.content_id,
        c.code,
        c.ord,
        c.token,
        c.token IN ('NR', 'UR', 'NA', 'NONE', 'UNRATED', 'UNKNOWN', 'NOTRATED', 'NOTYETRATED') AS unrated,
        CASE
            WHEN c.token IN ('G', 'TVY', 'TVG', 'U', 'UC', 'TOUSPUBLICS', 'TP', 'AL', 'APTA', 'L', '0', 'FSK0', 'ATP', 'AA', 'ALL') THEN 0
            WHEN c.token = 'M3' THEN 3
            WHEN c.token IN ('6', 'FSK6', 'M6') THEN 6
            WHEN c.token IN ('TVY7', 'TVY7FV', '7', 'UA7', 'UA7+') THEN 7
            WHEN c.token IN ('PG', 'TVPG') THEN 8
            WHEN c.token = '9' THEN 9
            WHEN c.token = '10' THEN 10
            WHEN c.token IN ('12', '12A', 'FSK12', 'PG12', 'M12', 'UA') THEN 12
            WHEN c.token IN ('PG13', 'UA13', 'UA13+', 'SAM13') THEN 13
            WHEN c.token IN ('TV14', '14', 'M14') THEN 14
            WHEN c.token IN ('15', 'M', 'MA15', 'MA15+', 'R15', 'R15+', 'B15') THEN 15
            WHEN c.token IN ('16', 'FSK16', 'M16', 'UA16', 'UA16+', 'SAM16') THEN 16
            WHEN c.token IN ('R', 'TVMA') THEN 17
            WHEN c.token IN ('NC17', '18', 'FSK18', 'R18', 'R18+', 'X18', 'X18+', 'X', 'M18', 'SAM18') THEN 18
            -- A bare age, with an optional "+" on either side: "16", "16+".
            -- Anything above 21 is junk in a rating column, not an age.
            WHEN c.token ~ '^\+?[0-9]{1,2}\+?$'
                 AND SUBSTRING(c.token FROM '^\+?([0-9]{1,2})\+?$')::integer <= 21
                THEN SUBSTRING(c.token FROM '^\+?([0-9]{1,2})\+?$')::integer
            ELSE NULL::integer
        END AS age
    FROM candidates c
    WHERE c.token <> ''
),
-- The first candidate that resolves decides: an age, or an unrated marker
-- (no age). When no candidate resolves, the text is unrecognized and stores 99,
-- above every age a ceiling can reach, so no ceiling ever admits it.
resolved AS (
    SELECT DISTINCT ON (s.content_id)
        s.content_id,
        CASE
            WHEN s.unrated THEN NULL
            WHEN s.age IS NOT NULL THEN s.age
            ELSE 99
        END AS age
    FROM scored s
    ORDER BY s.content_id, (s.unrated OR s.age IS NOT NULL) DESC, s.ord
)
-- IS DISTINCT FROM makes a re-run cheap as well as correct: a concurrent index
-- build below can fail and leave the migration to be applied again, and without
-- the guard the retry rewrites every already-correct row plus all of that
-- table's index entries a second time.
UPDATE public.media_items mi
SET content_rating_age = r.age
FROM resolved r
WHERE r.content_id = mi.content_id
  AND r.age IS NOT NULL
  AND mi.content_rating_age IS DISTINCT FROM r.age;

-- Not a partial index: with access.unrated_content = allow the ceiling
-- predicate is "(content_rating_age IS NULL OR content_rating_age <= $n)", and
-- an index restricted to NOT NULL rows cannot answer the IS NULL branch. A
-- failed concurrent build leaves an invalid index, so drop before building.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_content_rating_age;
CREATE INDEX CONCURRENTLY idx_media_items_content_rating_age
ON public.media_items USING btree (content_rating_age);

-- Episode entries carry the parent series' age so the shared access filter can
-- apply a ceiling against the "ece" alias the same way it does against
-- media_items, and so the ceiling reads an indexed column.
--
-- content_rating_rank is dropped below: it held a bucket derived from the rating
-- string at query time, which the stored age replaces exactly. Sorting on the
-- age with SQL's default NULL ordering (last ascending, first descending) is the
-- same order 2147483647 produced for an unknown rating.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.refresh_episode_catalog_entry(p_episode_id text, p_media_folder_id integer)
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    IF p_episode_id IS NULL OR p_media_folder_id IS NULL THEN
        RETURN;
    END IF;

    INSERT INTO public.episode_catalog_entries (
        media_folder_id,
        episode_id,
        series_id,
        sort_key,
        title,
        added_at,
        episode_air_date,
        year,
        genres,
        studios,
        networks,
        countries,
        original_language,
        content_rating,
        content_rating_label,
        content_rating_age,
        status,
        runtime,
        rating_imdb,
        rating_tmdb,
        max_resolution_rank,
        resolution_codes,
        max_bitrate,
        min_bitrate,
        has_hdr,
        has_non_hdr,
        has_dolby_vision,
        has_non_dolby_vision,
        audio_language_codes,
        subtitle_language_codes,
        episode_created_at,
        updated_at
    )
    SELECT
        el.media_folder_id,
        e.content_id,
        e.series_id,
        LOWER(COALESCE(NULLIF(BTRIM(e.title), ''), 'Episode ' || e.episode_number::text)) AS sort_key,
        COALESCE(NULLIF(BTRIM(e.title), ''), 'Episode ' || e.episode_number::text) AS title,
        el.first_seen_at,
        e.air_date,
        COALESCE(si.year, EXTRACT(YEAR FROM e.air_date)::integer, 0) AS year,
        COALESCE(si.genres, '{}'::text[]) AS genres,
        COALESCE(si.studios, '{}'::text[]) AS studios,
        COALESCE(si.networks, '{}'::text[]) AS networks,
        COALESCE(si.countries, '{}'::text[]) AS countries,
        COALESCE(si.original_language, '') AS original_language,
        COALESCE(si.content_rating, '') AS content_rating,
        LOWER(COALESCE(NULLIF(BTRIM(si.content_rating), ''), '~~~~')) AS content_rating_label,
        si.content_rating_age,
        COALESCE(NULLIF(BTRIM(si.status), ''), 'matched') AS status,
        COALESCE(NULLIF(e.runtime, 0), COALESCE(si.runtime, 0)) AS runtime,
        e.rating_imdb,
        e.rating_tmdb,
        stats.max_resolution_rank,
        COALESCE(stats.resolution_codes, '{}'::text[]) AS resolution_codes,
        stats.max_bitrate,
        stats.min_bitrate,
        COALESCE(stats.has_hdr, false) AS has_hdr,
        COALESCE(stats.has_non_hdr, false) AS has_non_hdr,
        COALESCE(stats.has_dolby_vision, false) AS has_dolby_vision,
        COALESCE(stats.has_non_dolby_vision, false) AS has_non_dolby_vision,
        COALESCE(stats.audio_language_codes, '{}'::text[]) AS audio_language_codes,
        COALESCE(stats.subtitle_language_codes, '{}'::text[]) AS subtitle_language_codes,
        e.created_at,
        NOW()
    FROM public.episode_libraries el
    JOIN public.episodes e ON e.content_id = el.episode_id
    JOIN public.media_items si ON si.content_id = e.series_id
    LEFT JOIN LATERAL (
        SELECT
            MAX(public.episode_catalog_resolution_rank(mf.resolution)) AS max_resolution_rank,
            ARRAY(
                SELECT DISTINCT code
                FROM public.media_files mf_res
                CROSS JOIN LATERAL (
                    SELECT public.episode_catalog_normalized_resolution(mf_res.resolution) AS code
                ) normalized
                WHERE mf_res.episode_id = e.content_id
                  AND mf_res.media_folder_id = el.media_folder_id
                  AND mf_res.missing_since IS NULL
                  AND normalized.code IS NOT NULL
                ORDER BY code
            ) AS resolution_codes,
            MAX(mf.bitrate) FILTER (WHERE mf.bitrate IS NOT NULL AND mf.bitrate > 0) AS max_bitrate,
            MIN(mf.bitrate) FILTER (WHERE mf.bitrate IS NOT NULL AND mf.bitrate > 0) AS min_bitrate,
            BOOL_OR(mf.hdr IS TRUE) AS has_hdr,
            BOOL_OR(mf.hdr IS FALSE) AS has_non_hdr,
            BOOL_OR(EXISTS (
                SELECT 1
                FROM jsonb_array_elements(COALESCE(mf.video_tracks, '[]'::jsonb)) AS vt
                WHERE NULLIF(BTRIM(vt->>'dolby_vision'), '') IS NOT NULL
            )) AS has_dolby_vision,
            BOOL_OR(NOT EXISTS (
                SELECT 1
                FROM jsonb_array_elements(COALESCE(mf.video_tracks, '[]'::jsonb)) AS vt
                WHERE NULLIF(BTRIM(vt->>'dolby_vision'), '') IS NOT NULL
            )) AS has_non_dolby_vision,
            ARRAY(
                SELECT DISTINCT LOWER(NULLIF(BTRIM(lang), ''))
                FROM public.media_files mf_audio
                CROSS JOIN LATERAL UNNEST(COALESCE(mf_audio.audio_language_codes, '{}'::text[])) AS lang
                WHERE mf_audio.episode_id = e.content_id
                  AND mf_audio.media_folder_id = el.media_folder_id
                  AND mf_audio.missing_since IS NULL
                  AND NULLIF(BTRIM(lang), '') IS NOT NULL
                ORDER BY LOWER(NULLIF(BTRIM(lang), ''))
            ) AS audio_language_codes,
            ARRAY(
                SELECT DISTINCT lang_code
                FROM (
                    SELECT LOWER(NULLIF(BTRIM(lang), '')) AS lang_code
                    FROM public.media_files mf_sub
                    CROSS JOIN LATERAL UNNEST(COALESCE(mf_sub.subtitle_language_codes, '{}'::text[])) AS lang
                    WHERE mf_sub.episode_id = e.content_id
                      AND mf_sub.media_folder_id = el.media_folder_id
                      AND mf_sub.missing_since IS NULL
                    UNION
                    SELECT LOWER(NULLIF(BTRIM(track->>'language'), '')) AS lang_code
                    FROM public.media_files mf_ext
                    CROSS JOIN LATERAL jsonb_array_elements(COALESCE(mf_ext.external_subtitles, '[]'::jsonb)) AS track
                    WHERE mf_ext.episode_id = e.content_id
                      AND mf_ext.media_folder_id = el.media_folder_id
                      AND mf_ext.missing_since IS NULL
                ) subtitle_codes
                WHERE lang_code IS NOT NULL
                ORDER BY lang_code
            ) AS subtitle_language_codes
        FROM public.media_files mf
        WHERE mf.episode_id = e.content_id
          AND mf.media_folder_id = el.media_folder_id
          AND mf.missing_since IS NULL
    ) stats ON TRUE
    WHERE el.episode_id = p_episode_id
      AND el.media_folder_id = p_media_folder_id
    ON CONFLICT (media_folder_id, episode_id) DO UPDATE SET
        series_id = EXCLUDED.series_id,
        sort_key = EXCLUDED.sort_key,
        title = EXCLUDED.title,
        added_at = EXCLUDED.added_at,
        episode_air_date = EXCLUDED.episode_air_date,
        year = EXCLUDED.year,
        genres = EXCLUDED.genres,
        studios = EXCLUDED.studios,
        networks = EXCLUDED.networks,
        countries = EXCLUDED.countries,
        original_language = EXCLUDED.original_language,
        content_rating = EXCLUDED.content_rating,
        content_rating_label = EXCLUDED.content_rating_label,
        content_rating_age = EXCLUDED.content_rating_age,
        status = EXCLUDED.status,
        runtime = EXCLUDED.runtime,
        rating_imdb = EXCLUDED.rating_imdb,
        rating_tmdb = EXCLUDED.rating_tmdb,
        max_resolution_rank = EXCLUDED.max_resolution_rank,
        resolution_codes = EXCLUDED.resolution_codes,
        max_bitrate = EXCLUDED.max_bitrate,
        min_bitrate = EXCLUDED.min_bitrate,
        has_hdr = EXCLUDED.has_hdr,
        has_non_hdr = EXCLUDED.has_non_hdr,
        has_dolby_vision = EXCLUDED.has_dolby_vision,
        has_non_dolby_vision = EXCLUDED.has_non_dolby_vision,
        audio_language_codes = EXCLUDED.audio_language_codes,
        subtitle_language_codes = EXCLUDED.subtitle_language_codes,
        episode_created_at = EXCLUDED.episode_created_at,
        updated_at = NOW();

    IF NOT FOUND THEN
        DELETE FROM public.episode_catalog_entries
        WHERE episode_id = p_episode_id
          AND media_folder_id = p_media_folder_id;
    END IF;
END;
$$;
-- +goose StatementEnd

-- One statement, so no series update slips through between the drop and the
-- create.
-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_episode_catalog_entries_series ON public.media_items;
CREATE TRIGGER trg_episode_catalog_entries_series
AFTER UPDATE OF content_id, type, year, genres, studios, networks, countries, original_language, content_rating, content_rating_age, status, runtime ON public.media_items
FOR EACH ROW
WHEN (OLD.type = 'series' OR NEW.type = 'series')
EXECUTE FUNCTION public.episode_catalog_entries_series_trigger();
-- +goose StatementEnd

UPDATE public.episode_catalog_entries ece
SET content_rating_age = si.content_rating_age,
    updated_at = NOW()
FROM public.media_items si
WHERE si.content_id = ece.series_id
  AND ece.content_rating_age IS DISTINCT FROM si.content_rating_age;

-- Dropping the column drops the rank index with it; the index is then rebuilt
-- on the age.
ALTER TABLE public.episode_catalog_entries
    DROP COLUMN IF EXISTS content_rating_rank;

DROP INDEX CONCURRENTLY IF EXISTS public.idx_episode_catalog_entries_content_rating;
CREATE INDEX CONCURRENTLY idx_episode_catalog_entries_content_rating
ON public.episode_catalog_entries USING btree (media_folder_id, content_rating_age, content_rating_label, sort_key, episode_id);

-- episode_catalog_rating_rank existed only because the rank was derived from a
-- raw string at query time. With the age stored, nothing calls it, and leaving
-- an eleven-value US ladder in the schema invites the next reader to use it.
DROP FUNCTION IF EXISTS public.episode_catalog_rating_rank(text);

-- +goose Down
-- Restore the rank ladder and the rank column the Up dropped, verbatim from
-- 142_episode_catalog_entries.sql, before the refresh function that calls it.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.episode_catalog_rating_rank(rating text)
RETURNS integer
LANGUAGE sql
IMMUTABLE
AS $$
    SELECT CASE UPPER(NULLIF(BTRIM(rating), ''))
        WHEN 'G' THEN 0
        WHEN 'TV-Y' THEN 0
        WHEN 'TV-G' THEN 0
        WHEN 'PG' THEN 1
        WHEN 'TV-Y7' THEN 1
        WHEN 'TV-PG' THEN 1
        WHEN 'PG-13' THEN 2
        WHEN 'TV-14' THEN 2
        WHEN 'R' THEN 3
        WHEN 'NC-17' THEN 3
        WHEN 'TV-MA' THEN 3
        ELSE 2147483647
    END
$$;
-- +goose StatementEnd

ALTER TABLE public.episode_catalog_entries
    ADD COLUMN IF NOT EXISTS content_rating_rank integer NOT NULL DEFAULT 2147483647;

-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_episode_catalog_entries_series ON public.media_items;
CREATE TRIGGER trg_episode_catalog_entries_series
AFTER UPDATE OF content_id, type, year, genres, studios, networks, countries, original_language, content_rating, status, runtime ON public.media_items
FOR EACH ROW
WHEN (OLD.type = 'series' OR NEW.type = 'series')
EXECUTE FUNCTION public.episode_catalog_entries_series_trigger();
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.refresh_episode_catalog_entry(p_episode_id text, p_media_folder_id integer)
RETURNS void
LANGUAGE plpgsql
AS $$
BEGIN
    IF p_episode_id IS NULL OR p_media_folder_id IS NULL THEN
        RETURN;
    END IF;

    INSERT INTO public.episode_catalog_entries (
        media_folder_id,
        episode_id,
        series_id,
        sort_key,
        title,
        added_at,
        episode_air_date,
        year,
        genres,
        studios,
        networks,
        countries,
        original_language,
        content_rating,
        content_rating_label,
        content_rating_rank,
        status,
        runtime,
        rating_imdb,
        rating_tmdb,
        max_resolution_rank,
        resolution_codes,
        max_bitrate,
        min_bitrate,
        has_hdr,
        has_non_hdr,
        has_dolby_vision,
        has_non_dolby_vision,
        audio_language_codes,
        subtitle_language_codes,
        episode_created_at,
        updated_at
    )
    SELECT
        el.media_folder_id,
        e.content_id,
        e.series_id,
        LOWER(COALESCE(NULLIF(BTRIM(e.title), ''), 'Episode ' || e.episode_number::text)) AS sort_key,
        COALESCE(NULLIF(BTRIM(e.title), ''), 'Episode ' || e.episode_number::text) AS title,
        el.first_seen_at,
        e.air_date,
        COALESCE(si.year, EXTRACT(YEAR FROM e.air_date)::integer, 0) AS year,
        COALESCE(si.genres, '{}'::text[]) AS genres,
        COALESCE(si.studios, '{}'::text[]) AS studios,
        COALESCE(si.networks, '{}'::text[]) AS networks,
        COALESCE(si.countries, '{}'::text[]) AS countries,
        COALESCE(si.original_language, '') AS original_language,
        COALESCE(si.content_rating, '') AS content_rating,
        LOWER(COALESCE(NULLIF(BTRIM(si.content_rating), ''), '~~~~')) AS content_rating_label,
        public.episode_catalog_rating_rank(si.content_rating) AS content_rating_rank,
        COALESCE(NULLIF(BTRIM(si.status), ''), 'matched') AS status,
        COALESCE(NULLIF(e.runtime, 0), COALESCE(si.runtime, 0)) AS runtime,
        e.rating_imdb,
        e.rating_tmdb,
        stats.max_resolution_rank,
        COALESCE(stats.resolution_codes, '{}'::text[]) AS resolution_codes,
        stats.max_bitrate,
        stats.min_bitrate,
        COALESCE(stats.has_hdr, false) AS has_hdr,
        COALESCE(stats.has_non_hdr, false) AS has_non_hdr,
        COALESCE(stats.has_dolby_vision, false) AS has_dolby_vision,
        COALESCE(stats.has_non_dolby_vision, false) AS has_non_dolby_vision,
        COALESCE(stats.audio_language_codes, '{}'::text[]) AS audio_language_codes,
        COALESCE(stats.subtitle_language_codes, '{}'::text[]) AS subtitle_language_codes,
        e.created_at,
        NOW()
    FROM public.episode_libraries el
    JOIN public.episodes e ON e.content_id = el.episode_id
    JOIN public.media_items si ON si.content_id = e.series_id
    LEFT JOIN LATERAL (
        SELECT
            MAX(public.episode_catalog_resolution_rank(mf.resolution)) AS max_resolution_rank,
            ARRAY(
                SELECT DISTINCT code
                FROM public.media_files mf_res
                CROSS JOIN LATERAL (
                    SELECT public.episode_catalog_normalized_resolution(mf_res.resolution) AS code
                ) normalized
                WHERE mf_res.episode_id = e.content_id
                  AND mf_res.media_folder_id = el.media_folder_id
                  AND mf_res.missing_since IS NULL
                  AND normalized.code IS NOT NULL
                ORDER BY code
            ) AS resolution_codes,
            MAX(mf.bitrate) FILTER (WHERE mf.bitrate IS NOT NULL AND mf.bitrate > 0) AS max_bitrate,
            MIN(mf.bitrate) FILTER (WHERE mf.bitrate IS NOT NULL AND mf.bitrate > 0) AS min_bitrate,
            BOOL_OR(mf.hdr IS TRUE) AS has_hdr,
            BOOL_OR(mf.hdr IS FALSE) AS has_non_hdr,
            BOOL_OR(EXISTS (
                SELECT 1
                FROM jsonb_array_elements(COALESCE(mf.video_tracks, '[]'::jsonb)) AS vt
                WHERE NULLIF(BTRIM(vt->>'dolby_vision'), '') IS NOT NULL
            )) AS has_dolby_vision,
            BOOL_OR(NOT EXISTS (
                SELECT 1
                FROM jsonb_array_elements(COALESCE(mf.video_tracks, '[]'::jsonb)) AS vt
                WHERE NULLIF(BTRIM(vt->>'dolby_vision'), '') IS NOT NULL
            )) AS has_non_dolby_vision,
            ARRAY(
                SELECT DISTINCT LOWER(NULLIF(BTRIM(lang), ''))
                FROM public.media_files mf_audio
                CROSS JOIN LATERAL UNNEST(COALESCE(mf_audio.audio_language_codes, '{}'::text[])) AS lang
                WHERE mf_audio.episode_id = e.content_id
                  AND mf_audio.media_folder_id = el.media_folder_id
                  AND mf_audio.missing_since IS NULL
                  AND NULLIF(BTRIM(lang), '') IS NOT NULL
                ORDER BY LOWER(NULLIF(BTRIM(lang), ''))
            ) AS audio_language_codes,
            ARRAY(
                SELECT DISTINCT lang_code
                FROM (
                    SELECT LOWER(NULLIF(BTRIM(lang), '')) AS lang_code
                    FROM public.media_files mf_sub
                    CROSS JOIN LATERAL UNNEST(COALESCE(mf_sub.subtitle_language_codes, '{}'::text[])) AS lang
                    WHERE mf_sub.episode_id = e.content_id
                      AND mf_sub.media_folder_id = el.media_folder_id
                      AND mf_sub.missing_since IS NULL
                    UNION
                    SELECT LOWER(NULLIF(BTRIM(track->>'language'), '')) AS lang_code
                    FROM public.media_files mf_ext
                    CROSS JOIN LATERAL jsonb_array_elements(COALESCE(mf_ext.external_subtitles, '[]'::jsonb)) AS track
                    WHERE mf_ext.episode_id = e.content_id
                      AND mf_ext.media_folder_id = el.media_folder_id
                      AND mf_ext.missing_since IS NULL
                ) subtitle_codes
                WHERE lang_code IS NOT NULL
                ORDER BY lang_code
            ) AS subtitle_language_codes
        FROM public.media_files mf
        WHERE mf.episode_id = e.content_id
          AND mf.media_folder_id = el.media_folder_id
          AND mf.missing_since IS NULL
    ) stats ON TRUE
    WHERE el.episode_id = p_episode_id
      AND el.media_folder_id = p_media_folder_id
    ON CONFLICT (media_folder_id, episode_id) DO UPDATE SET
        series_id = EXCLUDED.series_id,
        sort_key = EXCLUDED.sort_key,
        title = EXCLUDED.title,
        added_at = EXCLUDED.added_at,
        episode_air_date = EXCLUDED.episode_air_date,
        year = EXCLUDED.year,
        genres = EXCLUDED.genres,
        studios = EXCLUDED.studios,
        networks = EXCLUDED.networks,
        countries = EXCLUDED.countries,
        original_language = EXCLUDED.original_language,
        content_rating = EXCLUDED.content_rating,
        content_rating_label = EXCLUDED.content_rating_label,
        content_rating_rank = EXCLUDED.content_rating_rank,
        status = EXCLUDED.status,
        runtime = EXCLUDED.runtime,
        rating_imdb = EXCLUDED.rating_imdb,
        rating_tmdb = EXCLUDED.rating_tmdb,
        max_resolution_rank = EXCLUDED.max_resolution_rank,
        resolution_codes = EXCLUDED.resolution_codes,
        max_bitrate = EXCLUDED.max_bitrate,
        min_bitrate = EXCLUDED.min_bitrate,
        has_hdr = EXCLUDED.has_hdr,
        has_non_hdr = EXCLUDED.has_non_hdr,
        has_dolby_vision = EXCLUDED.has_dolby_vision,
        has_non_dolby_vision = EXCLUDED.has_non_dolby_vision,
        audio_language_codes = EXCLUDED.audio_language_codes,
        subtitle_language_codes = EXCLUDED.subtitle_language_codes,
        episode_created_at = EXCLUDED.episode_created_at,
        updated_at = NOW();

    IF NOT FOUND THEN
        DELETE FROM public.episode_catalog_entries
        WHERE episode_id = p_episode_id
          AND media_folder_id = p_media_folder_id;
    END IF;
END;
$$;
-- +goose StatementEnd

UPDATE public.episode_catalog_entries ece
SET content_rating_rank = public.episode_catalog_rating_rank(ece.content_rating),
    updated_at = NOW()
WHERE ece.content_rating_rank IS DISTINCT FROM public.episode_catalog_rating_rank(ece.content_rating);

DROP INDEX CONCURRENTLY IF EXISTS public.idx_episode_catalog_entries_content_rating;
CREATE INDEX CONCURRENTLY idx_episode_catalog_entries_content_rating
ON public.episode_catalog_entries USING btree (media_folder_id, content_rating_rank, content_rating_label, sort_key, episode_id);

DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_items_content_rating_age;

ALTER TABLE public.episode_catalog_entries
    DROP COLUMN IF EXISTS content_rating_age;

ALTER TABLE public.media_items
    DROP COLUMN IF EXISTS content_rating_age;
