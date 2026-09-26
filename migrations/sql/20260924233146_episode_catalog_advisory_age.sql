-- +goose NO TRANSACTION

-- +goose Up
-- Episode catalog entries carry the parent series' advisory age, the way they
-- already carry its content_rating_age, so the shared access filter can apply a
-- profile's advisory-age limit against the "ece" alias exactly as it does
-- against media_items. Without the column, every episode listing on a profile
-- with a limit would fail.
--
-- 20260924171447_add_advisory_age.sql kept the advisory out of this table
-- because it was display only. A profile limit now reads it, so it is copied
-- here like any other column the access filter needs.
--
-- No index, here or on media_items. Advisory coverage is sparse, and the limit
-- admits a title with no advisory age, so "advisory_age IS NULL OR
-- advisory_age <= $n" matches nearly every row: it is a residual filter that no
-- btree would serve.
--
-- The file runs without a wrapping transaction, like
-- 20260923234321_content_rating_age.sql: the trigger swap takes a brief
-- exclusive lock on media_items that must not be held through the backfill.
-- The order closes the gap a concurrent series update could fall into: the
-- function copies the column before the trigger watches it, and the backfill
-- runs last. Every statement is safe to re-run after a partial failure.
ALTER TABLE public.episode_catalog_entries
    ADD COLUMN IF NOT EXISTS advisory_age smallint;

-- Only movies and series carry an advisory age: the host now drops one a
-- plugin reports for any other type, so the profile advisory-age limit can
-- never hide a title in the beta book libraries. A refresh never clears a
-- stored age, so clear any a book, podcast or other row picked up before that
-- rule existed. The provider that supplies advisory ages only answers for
-- movies and series, so this normally matches nothing. Down does not restore
-- them.
UPDATE public.media_items
SET advisory_age = NULL,
    advisory_source = NULL
WHERE type NOT IN ('movie', 'series')
  AND (advisory_age IS NOT NULL OR advisory_source IS NOT NULL);

-- Verbatim from 20260923234321_content_rating_age.sql, plus advisory_age.
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
        advisory_age,
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
        si.advisory_age,
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
        advisory_age = EXCLUDED.advisory_age,
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
AFTER UPDATE OF content_id, type, year, genres, studios, networks, countries, original_language, content_rating, content_rating_age, advisory_age, status, runtime ON public.media_items
FOR EACH ROW
WHEN (OLD.type = 'series' OR NEW.type = 'series')
EXECUTE FUNCTION public.episode_catalog_entries_series_trigger();
-- +goose StatementEnd

UPDATE public.episode_catalog_entries ece
SET advisory_age = si.advisory_age,
    updated_at = NOW()
FROM public.media_items si
WHERE si.content_id = ece.series_id
  AND ece.advisory_age IS DISTINCT FROM si.advisory_age;

-- +goose Down
-- The trigger and the function return to their 20260923234321 definitions
-- before the column they reference is dropped.
-- +goose StatementBegin
DROP TRIGGER IF EXISTS trg_episode_catalog_entries_series ON public.media_items;
CREATE TRIGGER trg_episode_catalog_entries_series
AFTER UPDATE OF content_id, type, year, genres, studios, networks, countries, original_language, content_rating, content_rating_age, status, runtime ON public.media_items
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

ALTER TABLE public.episode_catalog_entries
    DROP COLUMN IF EXISTS advisory_age;
