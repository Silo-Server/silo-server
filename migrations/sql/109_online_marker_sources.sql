-- +goose Up
-- +goose StatementBegin
ALTER TABLE media_files
    ADD COLUMN IF NOT EXISTS intro_markers_provider text,
    ADD COLUMN IF NOT EXISTS credits_markers_source text,
    ADD COLUMN IF NOT EXISTS credits_markers_provider text,
    ADD COLUMN IF NOT EXISTS credits_markers_confidence double precision,
    ADD COLUMN IF NOT EXISTS credits_markers_algorithm text,
    ADD COLUMN IF NOT EXISTS credits_markers_detected_at timestamp with time zone;

INSERT INTO server_settings (key, value) VALUES ('markers.mode', 'local')
ON CONFLICT (key) DO NOTHING;

INSERT INTO server_settings (key, value) VALUES ('markers.lazy_playback', 'false')
ON CONFLICT (key) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM server_settings
WHERE key IN ('markers.mode', 'markers.lazy_playback');

ALTER TABLE media_files
    DROP COLUMN IF EXISTS credits_markers_detected_at,
    DROP COLUMN IF EXISTS credits_markers_algorithm,
    DROP COLUMN IF EXISTS credits_markers_confidence,
    DROP COLUMN IF EXISTS credits_markers_provider,
    DROP COLUMN IF EXISTS credits_markers_source,
    DROP COLUMN IF EXISTS intro_markers_provider;
-- +goose StatementEnd
