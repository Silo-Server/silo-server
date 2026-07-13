-- +goose NO TRANSACTION

-- Pattern-ops indexes so the (media_folder_id, path-prefix LIKE) query family
-- — scan-state reads, unmatched claim drainers, queue syncs, availability
-- rebuilds, and the scoped present-library sync — resolves through an index
-- range scan instead of walking every row of the folder. file_path uses the
-- default (non-C) collation, so the plain btree unique index cannot serve
-- left-anchored LIKE; text_pattern_ops can. The observed_root_path twin is
-- needed because the series root queue sync ORs equality+LIKE across both
-- columns and a BitmapOr requires every arm to be indexable.
--
-- CONCURRENTLY caveat (same as 20260609120500): if the build fails midway it
-- leaves an INVALID index that IF NOT EXISTS will subsequently skip.
-- Remediation: DROP INDEX the invalid index manually and re-run.

-- +goose Up
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_files_folder_file_path_pattern
    ON media_files (media_folder_id, file_path text_pattern_ops);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_media_files_folder_observed_root_pattern
    ON media_files (media_folder_id, observed_root_path text_pattern_ops);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_media_files_folder_observed_root_pattern;
DROP INDEX CONCURRENTLY IF EXISTS idx_media_files_folder_file_path_pattern;
