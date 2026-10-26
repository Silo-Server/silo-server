-- +goose Up
-- Replace overlapping row triggers atomically. Overview-only edits update the
-- stored overview document without recomputing file/facet aggregates. File and
-- series refreshes update existing entries in place, so only a new entry
-- builds its search documents.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.refresh_episode_catalog_entry(p_episode_id text, p_media_folder_id integer)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    src record;
BEGIN
    IF p_episode_id IS NULL OR p_media_folder_id IS NULL THEN
        RETURN;
    END IF;

    SELECT
        e.series_id,
        LOWER(COALESCE(NULLIF(BTRIM(e.title), ''), 'Episode ' || e.episode_number::text)) AS sort_key,
        COALESCE(NULLIF(BTRIM(e.title), ''), 'Episode ' || e.episode_number::text) AS title,
        el.first_seen_at AS added_at,
        e.air_date AS episode_air_date,
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
        e.created_at AS episode_created_at
    INTO src
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
      AND el.media_folder_id = p_media_folder_id;

    IF NOT FOUND THEN
        DELETE FROM public.episode_catalog_entries
        WHERE episode_id = p_episode_id
          AND media_folder_id = p_media_folder_id;
        RETURN;
    END IF;

    -- Update an existing entry in place. An upsert fires the BEFORE INSERT
    -- trigger, which rebuilds both search documents before ON CONFLICT
    -- discards them. Retry when another session inserts or deletes this
    -- entry between the two statements.
    LOOP
        UPDATE public.episode_catalog_entries SET
            series_id = src.series_id,
            sort_key = src.sort_key,
            title = src.title,
            added_at = src.added_at,
            episode_air_date = src.episode_air_date,
            year = src.year,
            genres = src.genres,
            studios = src.studios,
            networks = src.networks,
            countries = src.countries,
            original_language = src.original_language,
            content_rating = src.content_rating,
            content_rating_label = src.content_rating_label,
            content_rating_age = src.content_rating_age,
            advisory_age = src.advisory_age,
            status = src.status,
            runtime = src.runtime,
            rating_imdb = src.rating_imdb,
            rating_tmdb = src.rating_tmdb,
            max_resolution_rank = src.max_resolution_rank,
            resolution_codes = src.resolution_codes,
            max_bitrate = src.max_bitrate,
            min_bitrate = src.min_bitrate,
            has_hdr = src.has_hdr,
            has_non_hdr = src.has_non_hdr,
            has_dolby_vision = src.has_dolby_vision,
            has_non_dolby_vision = src.has_non_dolby_vision,
            audio_language_codes = src.audio_language_codes,
            subtitle_language_codes = src.subtitle_language_codes,
            episode_created_at = src.episode_created_at,
            updated_at = NOW()
        WHERE media_folder_id = p_media_folder_id
          AND episode_id = p_episode_id;
        EXIT WHEN FOUND;

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
        ) VALUES (
            p_media_folder_id,
            p_episode_id,
            src.series_id,
            src.sort_key,
            src.title,
            src.added_at,
            src.episode_air_date,
            src.year,
            src.genres,
            src.studios,
            src.networks,
            src.countries,
            src.original_language,
            src.content_rating,
            src.content_rating_label,
            src.content_rating_age,
            src.advisory_age,
            src.status,
            src.runtime,
            src.rating_imdb,
            src.rating_tmdb,
            src.max_resolution_rank,
            src.resolution_codes,
            src.max_bitrate,
            src.min_bitrate,
            src.has_hdr,
            src.has_non_hdr,
            src.has_dolby_vision,
            src.has_non_dolby_vision,
            src.audio_language_codes,
            src.subtitle_language_codes,
            src.episode_created_at,
            NOW()
        )
        ON CONFLICT (media_folder_id, episode_id) DO NOTHING;
        EXIT WHEN FOUND;
    END LOOP;
END;
$$;

CREATE OR REPLACE FUNCTION public.set_episode_catalog_entry_search_fields()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE source_overview text;
BEGIN
    IF TG_OP = 'INSERT' OR NEW.episode_id IS DISTINCT FROM OLD.episode_id
       OR NEW.search_overview_vector IS NULL THEN
        SELECT COALESCE(e.overview, '') INTO source_overview
        FROM public.episodes e WHERE e.content_id = NEW.episode_id;
        NEW.search_overview_vector := to_tsvector('english', COALESCE(source_overview, ''));
    END IF;
    IF TG_OP = 'INSERT' OR NEW.episode_id IS DISTINCT FROM OLD.episode_id
       OR NEW.title IS DISTINCT FROM OLD.title
       OR NEW.search_title_normalized IS NULL OR NEW.search_title_vector IS NULL THEN
        NEW.search_title_normalized := public.normalize_search_text(NEW.title);
        NEW.search_title_vector := setweight(to_tsvector('simple', NEW.search_title_normalized), 'A');
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.episode_catalog_entries_episodes_trigger()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE catalog_changed boolean; overview_changed boolean;
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM public.episode_catalog_entries WHERE episode_id = OLD.content_id;
        RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE' THEN
        catalog_changed := ROW(NEW.content_id, NEW.series_id, NEW.title,
            NEW.episode_number, NEW.air_date, NEW.runtime, NEW.rating_imdb,
            NEW.rating_tmdb, NEW.still_path, NEW.still_thumbhash, NEW.created_at)
            IS DISTINCT FROM ROW(OLD.content_id, OLD.series_id, OLD.title,
            OLD.episode_number, OLD.air_date, OLD.runtime, OLD.rating_imdb,
            OLD.rating_tmdb, OLD.still_path, OLD.still_thumbhash, OLD.created_at);
        overview_changed := NEW.overview IS DISTINCT FROM OLD.overview;
        IF NOT catalog_changed THEN
            IF overview_changed THEN
                UPDATE public.episode_catalog_entries
                SET search_overview_vector = to_tsvector('english', COALESCE(NEW.overview, '')),
                    updated_at = NOW()
                WHERE episode_id = NEW.content_id;
            END IF;
            RETURN NEW;
        END IF;
    END IF;
    PERFORM public.refresh_episode_catalog_entries_for_episode(NEW.content_id);
    IF TG_OP = 'UPDATE' AND OLD.content_id IS DISTINCT FROM NEW.content_id THEN
        DELETE FROM public.episode_catalog_entries WHERE episode_id = OLD.content_id;
    END IF;
    IF TG_OP = 'UPDATE' AND overview_changed THEN
        UPDATE public.episode_catalog_entries
        SET search_overview_vector = to_tsvector('english', COALESCE(NEW.overview, '')),
                    updated_at = NOW()
        WHERE episode_id = NEW.content_id;
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.episode_catalog_entries_media_files_trigger()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        PERFORM public.refresh_episode_catalog_entry(OLD.episode_id, OLD.media_folder_id);
        RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE' AND
       (OLD.episode_id IS DISTINCT FROM NEW.episode_id OR OLD.media_folder_id IS DISTINCT FROM NEW.media_folder_id) THEN
        PERFORM public.refresh_episode_catalog_entry(OLD.episode_id, OLD.media_folder_id);
    END IF;
    PERFORM public.refresh_episode_catalog_entry(NEW.episode_id, NEW.media_folder_id);
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION public.episode_catalog_entries_series_trigger()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM public.episode_catalog_entries WHERE series_id = OLD.content_id;
        RETURN OLD;
    END IF;
    IF ROW(NEW.content_id, NEW.type, NEW.year, NEW.genres, NEW.studios,
           NEW.networks, NEW.countries, NEW.original_language, NEW.content_rating,
           NEW.content_rating_age, NEW.advisory_age, NEW.status, NEW.runtime)
       IS NOT DISTINCT FROM ROW(OLD.content_id, OLD.type, OLD.year, OLD.genres, OLD.studios,
           OLD.networks, OLD.countries, OLD.original_language, OLD.content_rating,
           OLD.content_rating_age, OLD.advisory_age, OLD.status, OLD.runtime) THEN
        RETURN NEW;
    END IF;
    IF COALESCE(NEW.type, '') = 'series' THEN
        PERFORM public.refresh_episode_catalog_entries_for_series(NEW.content_id);
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_episode_catalog_entries_episodes_overview ON public.episodes;
DROP TRIGGER IF EXISTS trg_episode_catalog_entries_episodes ON public.episodes;
CREATE TRIGGER trg_episode_catalog_entries_episodes
AFTER INSERT OR UPDATE OF content_id, series_id, title, overview, episode_number,
    air_date, runtime, rating_imdb, rating_tmdb, still_path, still_thumbhash, created_at OR DELETE
ON public.episodes FOR EACH ROW
EXECUTE FUNCTION public.episode_catalog_entries_episodes_trigger();
-- +goose StatementEnd

-- +goose Down
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

CREATE OR REPLACE FUNCTION public.set_episode_catalog_entry_search_fields()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    source_overview text;
BEGIN
    SELECT COALESCE(e.overview, '')
    INTO source_overview
    FROM public.episodes e
    WHERE e.content_id = NEW.episode_id;

    NEW.search_title_normalized := public.normalize_search_text(NEW.title);
    NEW.search_title_vector := setweight(
        to_tsvector('simple', NEW.search_title_normalized),
        'A'
    );
    NEW.search_overview_vector := to_tsvector('english', COALESCE(source_overview, ''));
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_episode_catalog_entries_episodes ON public.episodes;
CREATE TRIGGER trg_episode_catalog_entries_episodes
AFTER INSERT OR UPDATE OF content_id, series_id, title, episode_number, air_date, runtime,
    rating_imdb, rating_tmdb, still_path, still_thumbhash, created_at OR DELETE
ON public.episodes FOR EACH ROW EXECUTE FUNCTION public.episode_catalog_entries_episodes_trigger();
CREATE TRIGGER trg_episode_catalog_entries_episodes_overview
AFTER UPDATE OF overview ON public.episodes FOR EACH ROW
EXECUTE FUNCTION public.episode_catalog_entries_episodes_trigger();
-- +goose StatementEnd
