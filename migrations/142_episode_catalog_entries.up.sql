CREATE TABLE IF NOT EXISTS public.episode_catalog_entries (
    media_folder_id integer NOT NULL,
    episode_id text NOT NULL,
    series_id text NOT NULL,
    sort_key text NOT NULL,
    title text NOT NULL,
    added_at timestamp with time zone NOT NULL,
    episode_air_date date,
    year integer NOT NULL,
    genres text[] NOT NULL DEFAULT '{}',
    studios text[] NOT NULL DEFAULT '{}',
    networks text[] NOT NULL DEFAULT '{}',
    countries text[] NOT NULL DEFAULT '{}',
    original_language text NOT NULL DEFAULT '',
    content_rating text NOT NULL DEFAULT '',
    content_rating_label text NOT NULL DEFAULT '~~~~',
    content_rating_rank integer NOT NULL DEFAULT 2147483647,
    status text NOT NULL DEFAULT 'matched',
    runtime integer NOT NULL DEFAULT 0,
    rating_imdb double precision,
    rating_tmdb double precision,
    max_resolution_rank integer,
    resolution_codes text[] NOT NULL DEFAULT '{}',
    max_bitrate integer,
    min_bitrate integer,
    has_hdr boolean NOT NULL DEFAULT false,
    has_non_hdr boolean NOT NULL DEFAULT false,
    has_dolby_vision boolean NOT NULL DEFAULT false,
    has_non_dolby_vision boolean NOT NULL DEFAULT false,
    audio_language_codes text[] NOT NULL DEFAULT '{}',
    subtitle_language_codes text[] NOT NULL DEFAULT '{}',
    episode_created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT episode_catalog_entries_pkey PRIMARY KEY (media_folder_id, episode_id),
    CONSTRAINT episode_catalog_entries_episode_id_fkey FOREIGN KEY (episode_id) REFERENCES public.episodes(content_id) ON DELETE CASCADE,
    CONSTRAINT episode_catalog_entries_media_folder_id_fkey FOREIGN KEY (media_folder_id) REFERENCES public.media_folders(id) ON DELETE CASCADE
);

CREATE TEMP TABLE episode_catalog_entries_migration_clock AS
SELECT transaction_timestamp() AS started_at;

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

CREATE OR REPLACE FUNCTION public.episode_catalog_normalized_resolution(resolution text)
RETURNS text
LANGUAGE sql
IMMUTABLE
AS $$
    SELECT CASE LOWER(NULLIF(BTRIM(resolution), ''))
        WHEN '4k' THEN '2160p'
        WHEN 'uhd' THEN '2160p'
        ELSE LOWER(NULLIF(BTRIM(resolution), ''))
    END
$$;

CREATE OR REPLACE FUNCTION public.episode_catalog_resolution_rank(resolution text)
RETURNS integer
LANGUAGE sql
IMMUTABLE
AS $$
    SELECT CASE UPPER(public.episode_catalog_normalized_resolution(resolution))
        WHEN '480P' THEN 1
        WHEN '720P' THEN 2
        WHEN '1080P' THEN 3
        WHEN '2160P' THEN 4
        WHEN '4320P' THEN 5
        ELSE NULL
    END
$$;

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
            BOOL_OR(COALESCE(mf.hdr, false)) AS has_hdr,
            BOOL_OR(NOT COALESCE(mf.hdr, false)) AS has_non_hdr,
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

CREATE OR REPLACE FUNCTION public.refresh_episode_catalog_entries_for_episode(p_episode_id text)
RETURNS void
LANGUAGE sql
AS $$
    SELECT public.refresh_episode_catalog_entry(el.episode_id, el.media_folder_id)
    FROM public.episode_libraries el
    WHERE el.episode_id = p_episode_id;
$$;

CREATE OR REPLACE FUNCTION public.refresh_episode_catalog_entries_for_series(p_series_id text)
RETURNS void
LANGUAGE sql
AS $$
    SELECT public.refresh_episode_catalog_entry(el.episode_id, el.media_folder_id)
    FROM public.episode_libraries el
    JOIN public.episodes e ON e.content_id = el.episode_id
    WHERE e.series_id = p_series_id;
$$;

CREATE OR REPLACE FUNCTION public.episode_catalog_entries_episode_libraries_trigger()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM public.episode_catalog_entries
        WHERE episode_id = OLD.episode_id
          AND media_folder_id = OLD.media_folder_id;
        RETURN OLD;
    END IF;

    PERFORM public.refresh_episode_catalog_entry(NEW.episode_id, NEW.media_folder_id);
    IF TG_OP = 'UPDATE'
       AND (OLD.episode_id IS DISTINCT FROM NEW.episode_id OR OLD.media_folder_id IS DISTINCT FROM NEW.media_folder_id) THEN
        PERFORM public.refresh_episode_catalog_entry(OLD.episode_id, OLD.media_folder_id);
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.episode_catalog_entries_media_files_trigger()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        PERFORM public.refresh_episode_catalog_entry(OLD.episode_id, OLD.media_folder_id);
        RETURN OLD;
    END IF;

    IF TG_OP = 'UPDATE' THEN
        PERFORM public.refresh_episode_catalog_entry(OLD.episode_id, OLD.media_folder_id);
    END IF;

    PERFORM public.refresh_episode_catalog_entry(NEW.episode_id, NEW.media_folder_id);
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.episode_catalog_entries_episodes_trigger()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM public.episode_catalog_entries
        WHERE episode_id = OLD.content_id;
        RETURN OLD;
    END IF;

    PERFORM public.refresh_episode_catalog_entries_for_episode(NEW.content_id);
    IF TG_OP = 'UPDATE' AND OLD.content_id IS DISTINCT FROM NEW.content_id THEN
        DELETE FROM public.episode_catalog_entries
        WHERE episode_id = OLD.content_id;
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.episode_catalog_entries_series_trigger()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM public.episode_catalog_entries
        WHERE series_id = OLD.content_id;
        RETURN OLD;
    END IF;

    IF COALESCE(NEW.type, '') = 'series' THEN
        PERFORM public.refresh_episode_catalog_entries_for_series(NEW.content_id);
    END IF;
    RETURN NEW;
END;
$$;

WITH active_episode_files AS (
    SELECT
        mf.episode_id,
        mf.media_folder_id,
        public.episode_catalog_normalized_resolution(mf.resolution) AS normalized_resolution,
        public.episode_catalog_resolution_rank(mf.resolution) AS resolution_rank,
        mf.bitrate,
        COALESCE(mf.hdr, false) AS hdr,
        EXISTS (
            SELECT 1
            FROM jsonb_array_elements(COALESCE(mf.video_tracks, '[]'::jsonb)) AS vt
            WHERE NULLIF(BTRIM(vt->>'dolby_vision'), '') IS NOT NULL
        ) AS has_dolby_vision,
        mf.audio_language_codes,
        mf.subtitle_language_codes,
        mf.external_subtitles
    FROM public.media_files mf
    WHERE mf.episode_id IS NOT NULL
      AND mf.missing_since IS NULL
),
file_stats AS (
    SELECT
        episode_id,
        media_folder_id,
        MAX(resolution_rank) AS max_resolution_rank,
        ARRAY_AGG(DISTINCT normalized_resolution ORDER BY normalized_resolution)
            FILTER (WHERE normalized_resolution IS NOT NULL) AS resolution_codes,
        MAX(bitrate) FILTER (WHERE bitrate IS NOT NULL AND bitrate > 0) AS max_bitrate,
        MIN(bitrate) FILTER (WHERE bitrate IS NOT NULL AND bitrate > 0) AS min_bitrate,
        BOOL_OR(hdr) AS has_hdr,
        BOOL_OR(NOT hdr) AS has_non_hdr,
        BOOL_OR(has_dolby_vision) AS has_dolby_vision,
        BOOL_OR(NOT has_dolby_vision) AS has_non_dolby_vision
    FROM active_episode_files
    GROUP BY episode_id, media_folder_id
),
audio_stats AS (
    SELECT
        episode_id,
        media_folder_id,
        ARRAY_AGG(DISTINCT lang_code ORDER BY lang_code) AS audio_language_codes
    FROM (
        SELECT
            aef.episode_id,
            aef.media_folder_id,
            LOWER(NULLIF(BTRIM(lang), '')) AS lang_code
        FROM active_episode_files aef
        CROSS JOIN LATERAL UNNEST(COALESCE(aef.audio_language_codes, '{}'::text[])) AS lang
    ) codes
    WHERE lang_code IS NOT NULL
    GROUP BY episode_id, media_folder_id
),
subtitle_stats AS (
    SELECT
        episode_id,
        media_folder_id,
        ARRAY_AGG(DISTINCT lang_code ORDER BY lang_code) AS subtitle_language_codes
    FROM (
        SELECT
            aef.episode_id,
            aef.media_folder_id,
            LOWER(NULLIF(BTRIM(lang), '')) AS lang_code
        FROM active_episode_files aef
        CROSS JOIN LATERAL UNNEST(COALESCE(aef.subtitle_language_codes, '{}'::text[])) AS lang
        UNION
        SELECT
            aef.episode_id,
            aef.media_folder_id,
            LOWER(NULLIF(BTRIM(track->>'language'), '')) AS lang_code
        FROM active_episode_files aef
        CROSS JOIN LATERAL jsonb_array_elements(COALESCE(aef.external_subtitles, '[]'::jsonb)) AS track
    ) codes
    WHERE lang_code IS NOT NULL
    GROUP BY episode_id, media_folder_id
)
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
    file_stats.max_resolution_rank,
    COALESCE(file_stats.resolution_codes, '{}'::text[]) AS resolution_codes,
    file_stats.max_bitrate,
    file_stats.min_bitrate,
    COALESCE(file_stats.has_hdr, false) AS has_hdr,
    COALESCE(file_stats.has_non_hdr, false) AS has_non_hdr,
    COALESCE(file_stats.has_dolby_vision, false) AS has_dolby_vision,
    COALESCE(file_stats.has_non_dolby_vision, false) AS has_non_dolby_vision,
    COALESCE(audio_stats.audio_language_codes, '{}'::text[]) AS audio_language_codes,
    COALESCE(subtitle_stats.subtitle_language_codes, '{}'::text[]) AS subtitle_language_codes,
    e.created_at,
    NOW()
FROM public.episode_libraries el
JOIN public.episodes e ON e.content_id = el.episode_id
JOIN public.media_items si ON si.content_id = e.series_id
LEFT JOIN file_stats ON file_stats.episode_id = el.episode_id AND file_stats.media_folder_id = el.media_folder_id
LEFT JOIN audio_stats ON audio_stats.episode_id = el.episode_id AND audio_stats.media_folder_id = el.media_folder_id
LEFT JOIN subtitle_stats ON subtitle_stats.episode_id = el.episode_id AND subtitle_stats.media_folder_id = el.media_folder_id
ON CONFLICT (media_folder_id, episode_id) DO NOTHING;

CREATE INDEX IF NOT EXISTS idx_episode_catalog_entries_title
ON public.episode_catalog_entries USING btree (media_folder_id, sort_key, episode_id);

CREATE INDEX IF NOT EXISTS idx_episode_catalog_entries_added
ON public.episode_catalog_entries USING btree (media_folder_id, added_at DESC, sort_key, episode_id);

CREATE INDEX IF NOT EXISTS idx_episode_catalog_entries_air_date
ON public.episode_catalog_entries USING btree (media_folder_id, episode_air_date DESC NULLS LAST, sort_key, episode_id);

CREATE INDEX IF NOT EXISTS idx_episode_catalog_entries_year
ON public.episode_catalog_entries USING btree (media_folder_id, year DESC, sort_key, episode_id);

CREATE INDEX IF NOT EXISTS idx_episode_catalog_entries_content_rating
ON public.episode_catalog_entries USING btree (media_folder_id, content_rating_rank, content_rating_label, sort_key, episode_id);

CREATE INDEX IF NOT EXISTS idx_episode_catalog_entries_runtime
ON public.episode_catalog_entries USING btree (media_folder_id, runtime DESC NULLS LAST, sort_key, episode_id);

CREATE INDEX IF NOT EXISTS idx_episode_catalog_entries_imdb
ON public.episode_catalog_entries USING btree (media_folder_id, rating_imdb DESC NULLS LAST, sort_key, episode_id);

CREATE INDEX IF NOT EXISTS idx_episode_catalog_entries_tmdb
ON public.episode_catalog_entries USING btree (media_folder_id, rating_tmdb DESC NULLS LAST, sort_key, episode_id);

CREATE INDEX IF NOT EXISTS idx_episode_catalog_entries_resolution
ON public.episode_catalog_entries USING btree (media_folder_id, max_resolution_rank DESC NULLS LAST, sort_key, episode_id);

CREATE INDEX IF NOT EXISTS idx_episode_catalog_entries_bitrate
ON public.episode_catalog_entries USING btree (media_folder_id, max_bitrate DESC NULLS LAST, sort_key, episode_id);

CREATE INDEX IF NOT EXISTS idx_episode_catalog_entries_status
ON public.episode_catalog_entries USING btree (media_folder_id, status, sort_key, episode_id);

CREATE INDEX IF NOT EXISTS idx_episode_catalog_entries_hdr
ON public.episode_catalog_entries USING btree (media_folder_id, sort_key, episode_id)
WHERE has_hdr;

CREATE INDEX IF NOT EXISTS idx_episode_catalog_entries_non_hdr
ON public.episode_catalog_entries USING btree (media_folder_id, sort_key, episode_id)
WHERE has_non_hdr;

CREATE INDEX IF NOT EXISTS idx_episode_catalog_entries_dolby_vision
ON public.episode_catalog_entries USING btree (media_folder_id, sort_key, episode_id)
WHERE has_dolby_vision;

CREATE INDEX IF NOT EXISTS idx_episode_catalog_entries_non_dolby_vision
ON public.episode_catalog_entries USING btree (media_folder_id, sort_key, episode_id)
WHERE has_non_dolby_vision;

CREATE INDEX IF NOT EXISTS idx_episode_catalog_entries_genres_gin
ON public.episode_catalog_entries USING gin (genres);

CREATE INDEX IF NOT EXISTS idx_episode_catalog_entries_studios_gin
ON public.episode_catalog_entries USING gin (studios);

CREATE INDEX IF NOT EXISTS idx_episode_catalog_entries_networks_gin
ON public.episode_catalog_entries USING gin (networks);

CREATE INDEX IF NOT EXISTS idx_episode_catalog_entries_countries_gin
ON public.episode_catalog_entries USING gin (countries);

CREATE INDEX IF NOT EXISTS idx_episode_catalog_entries_resolution_codes_gin
ON public.episode_catalog_entries USING gin (resolution_codes);

CREATE INDEX IF NOT EXISTS idx_episode_catalog_entries_audio_gin
ON public.episode_catalog_entries USING gin (audio_language_codes);

CREATE INDEX IF NOT EXISTS idx_episode_catalog_entries_subtitle_gin
ON public.episode_catalog_entries USING gin (subtitle_language_codes);

DROP TRIGGER IF EXISTS trg_episode_catalog_entries_episode_libraries ON public.episode_libraries;
CREATE TRIGGER trg_episode_catalog_entries_episode_libraries
AFTER INSERT OR UPDATE OR DELETE ON public.episode_libraries
FOR EACH ROW EXECUTE FUNCTION public.episode_catalog_entries_episode_libraries_trigger();

DROP TRIGGER IF EXISTS trg_episode_catalog_entries_media_files ON public.media_files;
CREATE TRIGGER trg_episode_catalog_entries_media_files
AFTER INSERT OR UPDATE OF episode_id, media_folder_id, missing_since, resolution, bitrate, hdr, video_tracks, audio_tracks, subtitle_tracks, external_subtitles, created_at OR DELETE
ON public.media_files
FOR EACH ROW EXECUTE FUNCTION public.episode_catalog_entries_media_files_trigger();

DROP TRIGGER IF EXISTS trg_episode_catalog_entries_episodes ON public.episodes;
CREATE TRIGGER trg_episode_catalog_entries_episodes
AFTER INSERT OR UPDATE OF content_id, series_id, title, air_date, runtime, rating_imdb, rating_tmdb, still_path, still_thumbhash, created_at OR DELETE
ON public.episodes
FOR EACH ROW EXECUTE FUNCTION public.episode_catalog_entries_episodes_trigger();

DROP TRIGGER IF EXISTS trg_episode_catalog_entries_series ON public.media_items;
CREATE TRIGGER trg_episode_catalog_entries_series
AFTER UPDATE OF content_id, type, year, genres, studios, networks, countries, original_language, content_rating, status, runtime ON public.media_items
FOR EACH ROW
WHEN (OLD.type = 'series' OR NEW.type = 'series')
EXECUTE FUNCTION public.episode_catalog_entries_series_trigger();

WITH migration_clock AS (
    SELECT started_at FROM episode_catalog_entries_migration_clock LIMIT 1
),
changed_entries AS (
    SELECT DISTINCT mf.episode_id, mf.media_folder_id
    FROM public.media_files mf, migration_clock mc
    WHERE mf.episode_id IS NOT NULL
      AND mf.updated_at >= mc.started_at
    UNION
    SELECT DISTINCT el.episode_id, el.media_folder_id
    FROM public.episode_libraries el, migration_clock mc
    WHERE el.first_seen_at >= mc.started_at
    UNION
    SELECT DISTINCT el.episode_id, el.media_folder_id
    FROM public.episode_libraries el
    JOIN public.episodes e ON e.content_id = el.episode_id
    CROSS JOIN migration_clock mc
    WHERE e.updated_at >= mc.started_at
    UNION
    SELECT DISTINCT el.episode_id, el.media_folder_id
    FROM public.episode_libraries el
    JOIN public.episodes e ON e.content_id = el.episode_id
    JOIN public.media_items si ON si.content_id = e.series_id
    CROSS JOIN migration_clock mc
    WHERE si.updated_at >= mc.started_at
      AND si.type = 'series'
)
SELECT COUNT(*)
FROM (
    SELECT public.refresh_episode_catalog_entry(episode_id, media_folder_id)
    FROM changed_entries
) refreshed;
