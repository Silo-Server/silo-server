-- +goose Up
-- +goose StatementBegin
-- Counts a webhook's deliveries that ended in a non-retryable 4xx since its
-- last success, re-enable, or URL change. Three in a row auto-disable it.
ALTER TABLE notification_webhooks
    ADD COLUMN consecutive_rejections integer NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE notification_webhooks DROP COLUMN IF EXISTS consecutive_rejections;
-- +goose StatementEnd
