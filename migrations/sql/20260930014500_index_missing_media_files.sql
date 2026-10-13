-- +goose NO TRANSACTION
-- +goose Up
-- Every subtree scan empties the folder's trash and looks up the items of its
-- missing files. Only missing rows are indexed, so neither has to read every
-- file in a large library. A failed concurrent build leaves an invalid index,
-- so drop before building.
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_files_folder_missing;
CREATE INDEX CONCURRENTLY idx_media_files_folder_missing
ON public.media_files USING btree (media_folder_id, missing_since)
WHERE missing_since IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.idx_media_files_folder_missing;
