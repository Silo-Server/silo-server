-- +goose Up
-- Why a request was declined or cancelled, when a reason was given. It used to
-- live only in the request's event log and the decline notification, so the
-- requester could not see it on the request itself.
ALTER TABLE media_requests ADD COLUMN outcome_reason text NOT NULL DEFAULT '';

UPDATE media_requests r
SET outcome_reason = e.message
FROM (
    SELECT DISTINCT ON (request_id) request_id, event_type, message
    FROM media_request_events
    WHERE event_type IN ('outcome_declined', 'outcome_cancelled')
    ORDER BY request_id, id DESC
) e
WHERE r.id = e.request_id
  AND e.event_type = 'outcome_' || r.outcome
  AND e.message <> '';

-- +goose Down
ALTER TABLE media_requests DROP COLUMN outcome_reason;
