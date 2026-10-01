-- +goose Up
-- Replace overlapping row triggers atomically. Overview-only edits update the
-- stored overview document without recomputing file/facet aggregates.
-- +goose StatementBegin
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
