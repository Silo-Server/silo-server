-- +goose Up
-- +goose StatementBegin
-- A scan request that lands on a scope already being scanned is coalesced into
-- that run. The run may have walked the directory before the new file landed,
-- so the request records the trigger it arrived with here and the run enqueues
-- one follow-up scan of the same scope when it finishes. Empty means no
-- follow-up is owed.
ALTER TABLE scan_runs
    ADD COLUMN followup_trigger TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE scan_runs
    DROP COLUMN IF EXISTS followup_trigger;
-- +goose StatementEnd
