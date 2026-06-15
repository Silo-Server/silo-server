-- +goose Up
-- +goose StatementBegin
-- request.fulfilled notifications (docs/superpowers/plans/notifications/06,
-- item 2): notify the requesting profile once its media request is actually
-- present in the catalog.

-- Notify marker. NULL means "completed but the fulfillment notification has
-- not fired yet"; the reconcile task keeps presence-checking such requests and
-- stamps this after the delivery is created (or suppressed by preferences).
ALTER TABLE public.media_requests
    ADD COLUMN fulfilled_notified_at timestamptz;

-- Flood safety: requests completed before this feature shipped must never
-- notify. Stamp them as already handled so only future completions fire.
UPDATE public.media_requests
SET fulfilled_notified_at = COALESCE(completed_at, now())
WHERE status = 'completed';

-- The reconcile task scans for pending notifications on every run.
CREATE INDEX media_requests_fulfill_notify_idx
    ON public.media_requests (completed_at)
    WHERE status = 'completed' AND fulfilled_notified_at IS NULL;

-- Per-webhook opt-out for request notifications. Defaults on: every channel
-- the requester configured should tell them (06, item 2).
ALTER TABLE public.notification_webhooks
    ADD COLUMN notify_requests boolean NOT NULL DEFAULT true;

-- At-most-once per (profile, request): the operational insert path uses
-- ON CONFLICT DO NOTHING, so a reconcile crash-retry or multi-node race
-- dedupes here instead of double-notifying.
CREATE UNIQUE INDEX notification_deliveries_profile_request_key
    ON public.notification_deliveries (profile_id, (reason_flags->>'request_id'))
    WHERE type = 'request.fulfilled';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.notification_deliveries_profile_request_key;
ALTER TABLE public.notification_webhooks DROP COLUMN IF EXISTS notify_requests;
DROP INDEX IF EXISTS public.media_requests_fulfill_notify_idx;
ALTER TABLE public.media_requests DROP COLUMN IF EXISTS fulfilled_notified_at;
-- +goose StatementEnd
