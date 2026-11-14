-- +goose Up
-- The download server settings the requester picked for a request (for
-- example a quality profile), with the server they were offered on. Empty
-- means the requester picked nothing, as for every request before requester
-- options.
ALTER TABLE media_requests ADD COLUMN requester_choices jsonb NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE media_requests DROP COLUMN requester_choices;
