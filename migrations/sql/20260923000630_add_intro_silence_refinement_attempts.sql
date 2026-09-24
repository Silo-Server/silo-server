-- +goose Up
-- One row per file records the last chapter silence refinement that ran without
-- applying a better boundary. The backfill skips a file while its row still
-- matches the file identity, the marker range that was refined, and the silence
-- settings hash: forever after a clean no-improvement result, and until
-- retry_after after a failure.
CREATE TABLE intro_silence_refinement_attempts (
    media_file_id bigint PRIMARY KEY REFERENCES media_files(id) ON DELETE CASCADE,
    config_hash text NOT NULL,
    file_hash text NOT NULL,
    file_size bigint NOT NULL,
    duration_seconds double precision NOT NULL,
    intro_start double precision NOT NULL,
    intro_end double precision NOT NULL,
    status text NOT NULL CHECK (status IN ('no_improvement', 'failed')),
    failure_count integer NOT NULL DEFAULT 0,
    last_error text,
    attempted_at timestamp with time zone NOT NULL,
    retry_after timestamp with time zone
);

-- +goose Down
DROP TABLE IF EXISTS intro_silence_refinement_attempts;
