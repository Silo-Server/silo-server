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
