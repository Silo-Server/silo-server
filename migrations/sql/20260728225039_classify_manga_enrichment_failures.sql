-- +goose NO TRANSACTION
-- +goose Up
ALTER TABLE manga_enrichment_state
    ADD COLUMN IF NOT EXISTS last_error_class text,
    ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz;

ALTER TABLE manga_enrichment_state
    DROP CONSTRAINT IF EXISTS manga_enrichment_state_error_class_check;

ALTER TABLE manga_enrichment_state
    ADD CONSTRAINT manga_enrichment_state_error_class_check
        CHECK (last_error_class IS NULL OR last_error_class IN ('transient', 'rate_limited', 'permanent'))
        NOT VALID;

ALTER TABLE manga_enrichment_state
    VALIDATE CONSTRAINT manga_enrichment_state_error_class_check;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_manga_enrichment_state_next_attempt
    ON manga_enrichment_state (next_attempt_at)
    WHERE next_attempt_at IS NOT NULL;

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS idx_manga_enrichment_state_next_attempt;

ALTER TABLE manga_enrichment_state
    DROP CONSTRAINT IF EXISTS manga_enrichment_state_error_class_check,
    DROP COLUMN IF EXISTS next_attempt_at,
    DROP COLUMN IF EXISTS last_error_class;
