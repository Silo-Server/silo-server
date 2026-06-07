-- +goose Up
-- +goose StatementBegin
ALTER TABLE media_files
    ADD COLUMN IF NOT EXISTS recap_start double precision,
    ADD COLUMN IF NOT EXISTS recap_end double precision,
    ADD COLUMN IF NOT EXISTS recap_markers_source text,
    ADD COLUMN IF NOT EXISTS recap_markers_provider text,
    ADD COLUMN IF NOT EXISTS recap_markers_confidence double precision,
    ADD COLUMN IF NOT EXISTS recap_markers_algorithm text,
    ADD COLUMN IF NOT EXISTS recap_markers_detected_at timestamp with time zone,
    ADD COLUMN IF NOT EXISTS preview_start double precision,
    ADD COLUMN IF NOT EXISTS preview_end double precision,
    ADD COLUMN IF NOT EXISTS preview_markers_source text,
    ADD COLUMN IF NOT EXISTS preview_markers_provider text,
    ADD COLUMN IF NOT EXISTS preview_markers_confidence double precision,
    ADD COLUMN IF NOT EXISTS preview_markers_algorithm text,
    ADD COLUMN IF NOT EXISTS preview_markers_detected_at timestamp with time zone;

ALTER TABLE user_profiles
    ADD COLUMN IF NOT EXISTS auto_skip_recap boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS auto_play_next_preview boolean NOT NULL DEFAULT false;

INSERT INTO server_settings (key, value) VALUES ('introdb.api_key', '')
ON CONFLICT (key) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
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
-- +goose StatementEnd
