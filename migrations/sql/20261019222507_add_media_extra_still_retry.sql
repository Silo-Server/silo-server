-- When a generated frame for a local extra failed for a reason that may pass
-- (a timeout, a missing file, tone mapping turned off), still_source records
-- the failure and still_retry_after says when the extra stills task may try
-- again. NULL with a failure recorded means the file cannot be decoded and is
-- skipped until it changes.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE media_extras
    ADD COLUMN IF NOT EXISTS still_retry_after TIMESTAMPTZ;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE media_extras
    DROP COLUMN IF EXISTS still_retry_after;
-- +goose StatementEnd
