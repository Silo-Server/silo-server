-- +goose NO TRANSACTION

-- +goose Up
-- Season detail and season episode-list routes resolve episodes by
-- episodes.season_id, while the existing episode index is keyed by
-- (series_id, season_number, episode_number). On large catalogs this forced a
-- parallel scan of the full episodes table for each season page load.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_index i ON i.indexrelid = c.oid
        WHERE n.nspname = 'public'
          AND c.relname = 'idx_episodes_season_id_episode_number'
          AND NOT i.indisvalid
    ) THEN
        DROP INDEX public.idx_episodes_season_id_episode_number;
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_episodes_season_id_episode_number
ON public.episodes USING btree (season_id, episode_number);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_episodes_season_id_episode_number;
