-- +goose Up
-- A catalog refresh read its inputs without locking anything. While another
-- transaction's refresh of the same entry was uncommitted (an episode title or
-- Sort Title edit, a series update, or a change to another file of the
-- episode), it read the old values, waited on that entry row at its UPDATE,
-- and then wrote the old values over the new ones (#4239). The refresh now
-- locks the entry row before it reads. It takes no lock on the episode row:
-- two file refreshes of one episode would share that lock and deadlock as
-- soon as either transaction then updated the episode. The rest of the
-- function is unchanged from 20261115000355_episode_sort_title_sort_key.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.refresh_episode_catalog_entry(p_episode_id text, p_media_folder_id integer)
RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    src record;
    locked boolean;
BEGIN
    IF p_episode_id IS NULL OR p_media_folder_id IS NULL THEN
        RETURN;
    END IF;

    -- Each pass locks the entry, then reads every input and writes. Each
    -- write to an input (episode, series, file, or membership) refreshes this
    -- entry in its own transaction, so the lock makes a refresh wait for one
    -- in flight and then read what it committed. A missing entry has no row to
    -- lock: when another session inserts it first, the INSERT below conflicts
    -- and the pass starts over with the lock.
    LOOP
        PERFORM 1
        FROM public.episode_catalog_entries
        WHERE media_folder_id = p_media_folder_id
          AND episode_id = p_episode_id
        FOR NO KEY UPDATE;
        locked := FOUND;

        SELECT
            e.series_id,
            LOWER(COALESCE(NULLIF(BTRIM(e.sort_title), ''), NULLIF(BTRIM(e.title), ''), 'Episode ' || e.episode_number::text)) AS sort_key,
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
                    FROM public.episode_serving_files(e.content_id, el.media_folder_id) mf_res
                    CROSS JOIN LATERAL (
                        SELECT public.episode_catalog_normalized_resolution(mf_res.resolution) AS code
                    ) normalized
                    WHERE normalized.code IS NOT NULL
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
                    FROM public.episode_serving_files(e.content_id, el.media_folder_id) mf_audio
                    CROSS JOIN LATERAL UNNEST(COALESCE(mf_audio.audio_language_codes, '{}'::text[])) AS lang
                    WHERE NULLIF(BTRIM(lang), '') IS NOT NULL
                    ORDER BY LOWER(NULLIF(BTRIM(lang), ''))
                ) AS audio_language_codes,
                ARRAY(
                    SELECT DISTINCT lang_code
                    FROM (
                        SELECT LOWER(NULLIF(BTRIM(lang), '')) AS lang_code
                        FROM public.episode_serving_files(e.content_id, el.media_folder_id) mf_sub
                        CROSS JOIN LATERAL UNNEST(COALESCE(mf_sub.subtitle_language_codes, '{}'::text[])) AS lang
                        UNION
                        SELECT LOWER(NULLIF(BTRIM(track->>'language'), '')) AS lang_code
                        FROM public.episode_serving_files(e.content_id, el.media_folder_id) mf_ext
                        CROSS JOIN LATERAL jsonb_array_elements(COALESCE(mf_ext.external_subtitles, '[]'::jsonb)) AS track
                    ) subtitle_codes
                    WHERE lang_code IS NOT NULL
                    ORDER BY lang_code
                ) AS subtitle_language_codes
            FROM public.episode_serving_files(e.content_id, el.media_folder_id) mf
        ) stats ON TRUE
        WHERE el.episode_id = p_episode_id
          AND el.media_folder_id = p_media_folder_id;

        -- Delete only the entry locked above. One that appeared since belongs
        -- to a membership this pass's read was too early to see.
        IF NOT FOUND THEN
            IF locked THEN
                DELETE FROM public.episode_catalog_entries
                WHERE episode_id = p_episode_id
                  AND media_folder_id = p_media_folder_id;
            END IF;
            RETURN;
        END IF;

        -- Update an existing entry in place, and only when a stored value changed:
        -- rewriting an unchanged entry still writes a new row version and an entry
        -- in each of its indexes. Missing search documents still get rebuilt by the
        -- BEFORE UPDATE trigger. An upsert fires the BEFORE INSERT trigger, which
        -- rebuilds both search documents before ON CONFLICT discards them. The
        -- two ROW comparisons must list every column the UPDATE assigns.
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
        LOWER(COALESCE(NULLIF(BTRIM(e.sort_title), ''), NULLIF(BTRIM(e.title), ''), 'Episode ' || e.episode_number::text)) AS sort_key,
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
                FROM public.episode_serving_files(e.content_id, el.media_folder_id) mf_res
                CROSS JOIN LATERAL (
                    SELECT public.episode_catalog_normalized_resolution(mf_res.resolution) AS code
                ) normalized
                WHERE normalized.code IS NOT NULL
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
                FROM public.episode_serving_files(e.content_id, el.media_folder_id) mf_audio
                CROSS JOIN LATERAL UNNEST(COALESCE(mf_audio.audio_language_codes, '{}'::text[])) AS lang
                WHERE NULLIF(BTRIM(lang), '') IS NOT NULL
                ORDER BY LOWER(NULLIF(BTRIM(lang), ''))
            ) AS audio_language_codes,
            ARRAY(
                SELECT DISTINCT lang_code
                FROM (
                    SELECT LOWER(NULLIF(BTRIM(lang), '')) AS lang_code
                    FROM public.episode_serving_files(e.content_id, el.media_folder_id) mf_sub
                    CROSS JOIN LATERAL UNNEST(COALESCE(mf_sub.subtitle_language_codes, '{}'::text[])) AS lang
                    UNION
                    SELECT LOWER(NULLIF(BTRIM(track->>'language'), '')) AS lang_code
                    FROM public.episode_serving_files(e.content_id, el.media_folder_id) mf_ext
                    CROSS JOIN LATERAL jsonb_array_elements(COALESCE(mf_ext.external_subtitles, '[]'::jsonb)) AS track
                ) subtitle_codes
                WHERE lang_code IS NOT NULL
                ORDER BY lang_code
            ) AS subtitle_language_codes
        FROM public.episode_serving_files(e.content_id, el.media_folder_id) mf
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
-- +goose StatementEnd
