-- +goose NO TRANSACTION
-- +goose Up
-- File linking looks episodes up by the numbers their files are named with
-- (episodes.file_season_number, file_episode_number).
--
-- CONCURRENTLY avoids blocking episode writes during the build; it cannot run
-- inside a transaction, hence the NO TRANSACTION annotation above. The index
-- is dropped first so a retry after a failed build, which leaves an invalid
-- index behind, recovers.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_episodes_series_file_numbers;
CREATE INDEX CONCURRENTLY idx_episodes_series_file_numbers
ON public.episodes (series_id, file_season_number, file_episode_number)
WHERE file_season_number IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_episodes_series_file_numbers;
