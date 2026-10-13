-- +goose Up
-- Submission bookkeeping for approved requests. A server claims a request by
-- setting submit_lease_until before it calls the router plugin, so two servers
-- (or an admin approval and the reconciler) never submit the same request at
-- once. After a failed attempt the lease is cleared and next_submit_at holds
-- the backoff. last_reconciled_at orders the reconcile pass so every in-flight
-- request gets checked, not only the oldest batch.
ALTER TABLE media_requests
    ADD COLUMN submit_attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN submit_lease_until timestamptz,
    ADD COLUMN next_submit_at timestamptz,
    ADD COLUMN last_reconciled_at timestamptz;

-- +goose Down
ALTER TABLE media_requests
    DROP COLUMN last_reconciled_at,
    DROP COLUMN next_submit_at,
    DROP COLUMN submit_lease_until,
    DROP COLUMN submit_attempts;
