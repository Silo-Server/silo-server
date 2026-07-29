-- +goose Up
-- +goose StatementBegin
ALTER TABLE movie_match_queue
    ADD COLUMN state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'parked')),
    ADD COLUMN failure_kind text NOT NULL DEFAULT '',
    ADD COLUMN failure_detail jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN deterministic_attempt_count integer NOT NULL DEFAULT 0,
    ADD COLUMN input_fingerprint text NOT NULL DEFAULT '',
    ADD COLUMN matcher_revision integer NOT NULL DEFAULT 0,
    ADD COLUMN lease_token text NOT NULL DEFAULT '',
    ADD COLUMN parked_at timestamptz;

ALTER TABLE series_root_match_queue
    ADD COLUMN state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'parked')),
    ADD COLUMN failure_kind text NOT NULL DEFAULT '',
    ADD COLUMN failure_detail jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN deterministic_attempt_count integer NOT NULL DEFAULT 0,
    ADD COLUMN input_fingerprint text NOT NULL DEFAULT '',
    ADD COLUMN matcher_revision integer NOT NULL DEFAULT 0,
    ADD COLUMN lease_token text NOT NULL DEFAULT '',
    ADD COLUMN parked_at timestamptz;

CREATE INDEX idx_movie_match_queue_claimable
    ON movie_match_queue (available_at, last_attempted_at, media_folder_id, media_file_id)
    WHERE state = 'pending';
CREATE INDEX idx_series_root_match_queue_claimable
    ON series_root_match_queue (available_at, last_attempted_at, media_folder_id, observed_root_path)
    WHERE state = 'pending';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_series_root_match_queue_claimable;
DROP INDEX IF EXISTS idx_movie_match_queue_claimable;

ALTER TABLE series_root_match_queue
    DROP COLUMN IF EXISTS parked_at,
    DROP COLUMN IF EXISTS lease_token,
    DROP COLUMN IF EXISTS matcher_revision,
    DROP COLUMN IF EXISTS input_fingerprint,
    DROP COLUMN IF EXISTS deterministic_attempt_count,
    DROP COLUMN IF EXISTS failure_detail,
    DROP COLUMN IF EXISTS failure_kind,
    DROP COLUMN IF EXISTS state;

ALTER TABLE movie_match_queue
    DROP COLUMN IF EXISTS parked_at,
    DROP COLUMN IF EXISTS lease_token,
    DROP COLUMN IF EXISTS matcher_revision,
    DROP COLUMN IF EXISTS input_fingerprint,
    DROP COLUMN IF EXISTS deterministic_attempt_count,
    DROP COLUMN IF EXISTS failure_detail,
    DROP COLUMN IF EXISTS failure_kind,
    DROP COLUMN IF EXISTS state;
-- +goose StatementEnd
