-- +goose Up
-- +goose StatementBegin
-- Person name lookups (actor/director filters + ingest dedup)
CREATE INDEX IF NOT EXISTS idx_people_name_lower
ON public.people USING btree (LOWER(name));

-- Person substring search (Jellyfin client autocomplete)
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE INDEX IF NOT EXISTS idx_people_name_trgm
ON public.people USING gin (name public.gin_trgm_ops);

-- Default browse title sort (LOWER(COALESCE(NULLIF(BTRIM(sort_title),''), title)))
CREATE INDEX IF NOT EXISTS idx_media_items_sort_key
ON public.media_items USING btree (
    LOWER(COALESCE(NULLIF(BTRIM(sort_title), ''), title))
);

-- Movie release_date sort (covers ~183K movies; series fall back to first_air_date)
CREATE INDEX IF NOT EXISTS idx_media_items_release_date
ON public.media_items USING btree (release_date DESC NULLS LAST)
WHERE release_date IS NOT NULL;

-- media_items external IDs — currently seq-scan 219K rows on GetByExternalID
CREATE INDEX IF NOT EXISTS idx_media_items_tmdb_id
ON public.media_items USING btree (tmdb_id) WHERE tmdb_id <> '';

CREATE INDEX IF NOT EXISTS idx_media_items_imdb_id
ON public.media_items USING btree (imdb_id) WHERE imdb_id <> '';

CREATE INDEX IF NOT EXISTS idx_media_items_tvdb_id
ON public.media_items USING btree (tvdb_id) WHERE tvdb_id <> '';

-- Keywords filter (currently @> seq-scans without GIN)
CREATE INDEX IF NOT EXISTS idx_media_items_keywords
ON public.media_items USING gin (keywords);

-- Watch-progress hot lists (Continue Watching, mark-played history)
CREATE INDEX IF NOT EXISTS idx_uwp_profile_completed
ON public.user_watch_progress USING btree (user_id, profile_id, updated_at DESC)
WHERE completed = TRUE;

CREATE INDEX IF NOT EXISTS idx_uwp_profile_in_progress
ON public.user_watch_progress USING btree (user_id, profile_id, updated_at DESC)
WHERE completed = FALSE;

-- item_people composite for kind-filtered person joins
CREATE INDEX IF NOT EXISTS idx_item_people_person_kind
ON public.item_people USING btree (person_id, kind);

-- Drop redundant indexes (each duplicates a UNIQUE constraint's implicit index)
DROP INDEX IF EXISTS public.idx_episodes_series;
DROP INDEX IF EXISTS public.idx_seasons_series;
DROP INDEX IF EXISTS public.idx_api_keys_api_key;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Recreate dropped duplicate indexes (matching original 001 schema)
CREATE INDEX IF NOT EXISTS idx_api_keys_api_key
ON public.api_keys USING btree (api_key);

CREATE INDEX IF NOT EXISTS idx_seasons_series
ON public.seasons USING btree (series_id, season_number);

CREATE INDEX IF NOT EXISTS idx_episodes_series
ON public.episodes USING btree (series_id, season_number, episode_number);

-- Drop new indexes
DROP INDEX IF EXISTS public.idx_item_people_person_kind;
DROP INDEX IF EXISTS public.idx_uwp_profile_in_progress;
DROP INDEX IF EXISTS public.idx_uwp_profile_completed;
DROP INDEX IF EXISTS public.idx_media_items_keywords;
DROP INDEX IF EXISTS public.idx_media_items_tvdb_id;
DROP INDEX IF EXISTS public.idx_media_items_imdb_id;
DROP INDEX IF EXISTS public.idx_media_items_tmdb_id;
DROP INDEX IF EXISTS public.idx_media_items_release_date;
DROP INDEX IF EXISTS public.idx_media_items_sort_key;
DROP INDEX IF EXISTS public.idx_people_name_trgm;
-- idx_people_name_lower is owned by migration 007; do not drop on 102 rollback.
-- Note: pg_trgm extension is left installed; harmless if unused.
-- +goose StatementEnd
