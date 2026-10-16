-- +goose NO TRANSACTION
-- +goose Up
-- Next Up looks up each candidate episode's availability rows by episode_id
-- alone, for every library the viewer can reach. The primary key leads with
-- library_id, so without this index that lookup is one index descent per
-- library for every candidate.
--
-- CONCURRENTLY avoids blocking availability writes during the build; it
-- cannot run inside a transaction, hence the NO TRANSACTION annotation above.
-- The index is dropped first so a retry after a failed build, which leaves an
-- invalid index behind, recovers.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_episode_availability_episode;
CREATE INDEX CONCURRENTLY idx_episode_availability_episode
ON public.episode_availability USING btree (episode_id);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_episode_availability_episode;
