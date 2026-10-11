-- +goose Up
-- Catalog entries are refreshed in place by triggers on episodes, files,
-- memberships and series. Skip the write when the refreshed values match the
-- stored entry, and stop episode still edits from refreshing at all: entries
-- store no still columns, and catalog reads join the still from episodes.
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

    -- Update an existing entry in place, and only when a stored value changed:
    -- rewriting an unchanged entry still writes a new row version and an entry
    -- in each of its indexes. Missing search documents still get rebuilt by the
    -- BEFORE UPDATE trigger. An upsert fires the BEFORE INSERT trigger, which
    -- rebuilds both search documents before ON CONFLICT discards them. Retry
    -- when another session inserts or deletes this entry between statements.
    -- The two ROW comparisons must list every column the UPDATE assigns.
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
          AND episode_id = p_episode_id
          AND (ROW(series_id, sort_key, title, added_at, episode_air_date, year, genres,
                  studios, networks, countries, original_language, content_rating,
                  content_rating_label, content_rating_age, advisory_age, status,
                  runtime, rating_imdb, rating_tmdb, max_resolution_rank,
                  resolution_codes, max_bitrate, min_bitrate, has_hdr, has_non_hdr,
                  has_dolby_vision, has_non_dolby_vision, audio_language_codes,
                  subtitle_language_codes, episode_created_at)
              IS DISTINCT FROM ROW(src.series_id, src.sort_key, src.title, src.added_at,
                  src.episode_air_date, src.year, src.genres, src.studios, src.networks,
                  src.countries, src.original_language, src.content_rating,
                  src.content_rating_label, src.content_rating_age, src.advisory_age,
                  src.status, src.runtime, src.rating_imdb, src.rating_tmdb,
                  src.max_resolution_rank, src.resolution_codes, src.max_bitrate,
                  src.min_bitrate, src.has_hdr, src.has_non_hdr, src.has_dolby_vision,
                  src.has_non_dolby_vision, src.audio_language_codes,
                  src.subtitle_language_codes, src.episode_created_at)
            OR search_title_normalized IS NULL
            OR search_title_vector IS NULL
            OR search_overview_vector IS NULL);
        EXIT WHEN FOUND;

        -- The entry already holds these values.
        PERFORM 1
        FROM public.episode_catalog_entries
        WHERE media_folder_id = p_media_folder_id
          AND episode_id = p_episode_id
          AND ROW(series_id, sort_key, title, added_at, episode_air_date, year, genres,
                  studios, networks, countries, original_language, content_rating,
                  content_rating_label, content_rating_age, advisory_age, status,
                  runtime, rating_imdb, rating_tmdb, max_resolution_rank,
                  resolution_codes, max_bitrate, min_bitrate, has_hdr, has_non_hdr,
                  has_dolby_vision, has_non_dolby_vision, audio_language_codes,
                  subtitle_language_codes, episode_created_at)
              IS NOT DISTINCT FROM ROW(src.series_id, src.sort_key, src.title, src.added_at,
                  src.episode_air_date, src.year, src.genres, src.studios, src.networks,
                  src.countries, src.original_language, src.content_rating,
                  src.content_rating_label, src.content_rating_age, src.advisory_age,
                  src.status, src.runtime, src.rating_imdb, src.rating_tmdb,
                  src.max_resolution_rank, src.resolution_codes, src.max_bitrate,
                  src.min_bitrate, src.has_hdr, src.has_non_hdr, src.has_dolby_vision,
                  src.has_non_dolby_vision, src.audio_language_codes,
                  src.subtitle_language_codes, src.episode_created_at);
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
            NEW.rating_tmdb, NEW.created_at)
            IS DISTINCT FROM ROW(OLD.content_id, OLD.series_id, OLD.title,
            OLD.episode_number, OLD.air_date, OLD.runtime, OLD.rating_imdb,
            OLD.rating_tmdb, OLD.created_at);
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

DROP TRIGGER IF EXISTS trg_episode_catalog_entries_episodes ON public.episodes;
CREATE TRIGGER trg_episode_catalog_entries_episodes
AFTER INSERT OR UPDATE OF content_id, series_id, title, overview, episode_number,
    air_date, runtime, rating_imdb, rating_tmdb, created_at OR DELETE
ON public.episodes FOR EACH ROW
EXECUTE FUNCTION public.episode_catalog_entries_episodes_trigger();
-- +goose StatementEnd

-- +goose Down
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

DROP TRIGGER IF EXISTS trg_episode_catalog_entries_episodes ON public.episodes;
CREATE TRIGGER trg_episode_catalog_entries_episodes
AFTER INSERT OR UPDATE OF content_id, series_id, title, overview, episode_number,
    air_date, runtime, rating_imdb, rating_tmdb, still_path, still_thumbhash, created_at OR DELETE
ON public.episodes FOR EACH ROW
EXECUTE FUNCTION public.episode_catalog_entries_episodes_trigger();
-- +goose StatementEnd
