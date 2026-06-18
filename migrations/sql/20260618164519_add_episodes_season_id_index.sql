-- +goose NO TRANSACTION

-- +goose Up
-- Season detail and season episode-list routes resolve episodes by
-- episodes.season_id, while the existing episode index is keyed by
-- (series_id, season_number, episode_number). On large catalogs this forced a
-- parallel scan of the full episodes table for each season page load.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_episodes_season_id_episode_number
ON public.episodes USING btree (season_id, episode_number);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_episodes_season_id_episode_number;
