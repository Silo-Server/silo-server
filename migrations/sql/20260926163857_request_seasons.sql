-- +goose Up
-- The seasons a series request asks for. Empty means the whole series: every
-- request from before season requests, whose old rule (any episode in the
-- library fulfills it) still applies.
ALTER TABLE media_requests ADD COLUMN seasons integer[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE media_requests DROP COLUMN seasons;
