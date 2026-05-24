DELETE FROM server_settings WHERE key = 'introdb.api_key';

ALTER TABLE user_profiles
    DROP COLUMN IF EXISTS auto_play_next_preview,
    DROP COLUMN IF EXISTS auto_skip_recap;

ALTER TABLE media_files
    DROP COLUMN IF EXISTS preview_markers_detected_at,
    DROP COLUMN IF EXISTS preview_markers_algorithm,
    DROP COLUMN IF EXISTS preview_markers_confidence,
    DROP COLUMN IF EXISTS preview_markers_provider,
    DROP COLUMN IF EXISTS preview_markers_source,
    DROP COLUMN IF EXISTS preview_end,
    DROP COLUMN IF EXISTS preview_start,
    DROP COLUMN IF EXISTS recap_markers_detected_at,
    DROP COLUMN IF EXISTS recap_markers_algorithm,
    DROP COLUMN IF EXISTS recap_markers_confidence,
    DROP COLUMN IF EXISTS recap_markers_provider,
    DROP COLUMN IF EXISTS recap_markers_source,
    DROP COLUMN IF EXISTS recap_end,
    DROP COLUMN IF EXISTS recap_start;
