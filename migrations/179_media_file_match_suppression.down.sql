DROP INDEX IF EXISTS idx_media_files_unmatched_match_ready;

ALTER TABLE media_files
    DROP COLUMN IF EXISTS match_suppressed_at;
