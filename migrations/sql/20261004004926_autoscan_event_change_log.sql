-- +goose Up
-- +goose StatementBegin
-- Per-event record of the changes a poll or webhook delivery received: the
-- reported path, the path after the source's rewrites, and what happened to
-- it (queued, joined an existing scan, suppressed, unresolved with a reason).
-- The host writes at most a bounded number of entries per event;
-- change_log_truncated marks events that received more changes than were
-- recorded (changes_returned keeps the full count).
ALTER TABLE autoscan_events
    ADD COLUMN change_log jsonb NOT NULL DEFAULT '[]'::jsonb,
    ADD COLUMN change_log_truncated boolean NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE autoscan_events
    DROP COLUMN IF EXISTS change_log_truncated,
    DROP COLUMN IF EXISTS change_log;
-- +goose StatementEnd
