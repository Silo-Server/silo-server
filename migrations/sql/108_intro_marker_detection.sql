-- +goose Up
-- +goose StatementBegin
ALTER TABLE media_folders
    ADD COLUMN IF NOT EXISTS intro_detection_enabled boolean NOT NULL DEFAULT false;

ALTER TABLE media_files
    ADD COLUMN IF NOT EXISTS intro_markers_source text,
    ADD COLUMN IF NOT EXISTS intro_markers_confidence double precision,
    ADD COLUMN IF NOT EXISTS intro_markers_algorithm text,
    ADD COLUMN IF NOT EXISTS intro_markers_detected_at timestamp with time zone;

UPDATE media_files
SET intro_markers_source = markers_source,
    intro_markers_confidence = markers_confidence
WHERE intro_start IS NOT NULL
  AND intro_end IS NOT NULL
  AND markers_source IS NOT NULL
  AND intro_markers_source IS NULL;

CREATE TABLE IF NOT EXISTS media_intro_fingerprints (
    media_file_id integer NOT NULL REFERENCES media_files(id) ON DELETE CASCADE,
    file_hash text NOT NULL,
    file_size bigint,
    duration_seconds double precision NOT NULL,
    window_start_seconds double precision NOT NULL DEFAULT 0,
    window_end_seconds double precision NOT NULL,
    algorithm_version integer NOT NULL,
    config_hash text NOT NULL,
    fingerprint_format text NOT NULL,
    sample_duration_seconds double precision NOT NULL,
    point_count integer NOT NULL,
    points bytea NOT NULL,
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (media_file_id, algorithm_version, config_hash)
);

CREATE INDEX IF NOT EXISTS idx_media_intro_fingerprints_hash
    ON media_intro_fingerprints (file_hash, algorithm_version, config_hash);

CREATE TABLE IF NOT EXISTS intro_season_analysis_state (
    season_id text NOT NULL REFERENCES seasons(content_id) ON DELETE CASCADE,
    media_folder_id integer NOT NULL REFERENCES media_folders(id) ON DELETE CASCADE,
    analysis_group_key text NOT NULL,
    algorithm_version integer NOT NULL,
    config_hash text NOT NULL,
    input_signature text NOT NULL,
    episode_count integer NOT NULL,
    file_count integer NOT NULL,
    status text NOT NULL,
    markers_written integer NOT NULL DEFAULT 0,
    last_error text,
    analyzed_at timestamp with time zone NOT NULL DEFAULT now(),
    PRIMARY KEY (season_id, media_folder_id, analysis_group_key, algorithm_version, config_hash)
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS intro_season_analysis_state;
DROP INDEX IF EXISTS idx_media_intro_fingerprints_hash;
DROP TABLE IF EXISTS media_intro_fingerprints;

ALTER TABLE media_files
    DROP COLUMN IF EXISTS intro_markers_detected_at,
    DROP COLUMN IF EXISTS intro_markers_algorithm,
    DROP COLUMN IF EXISTS intro_markers_confidence,
    DROP COLUMN IF EXISTS intro_markers_source;

ALTER TABLE media_folders
    DROP COLUMN IF EXISTS intro_detection_enabled;
-- +goose StatementEnd
