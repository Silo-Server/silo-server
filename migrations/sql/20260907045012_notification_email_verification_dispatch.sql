-- +goose Up
-- +goose StatementBegin
-- Dispatch state for retained verification messages. A row is claimed by
-- exactly one worker (SKIP LOCKED) before the provider hand-off; a row left in
-- 'sending' past its lease is an uncertain send and is retried once with the
-- same message, accepting a possible duplicate email over a lost one.
ALTER TABLE public.notification_email_verifications
    ADD COLUMN dispatch_state text NOT NULL DEFAULT 'queued'
        CHECK (dispatch_state IN ('queued', 'sending', 'delivered', 'failed')),
    ADD COLUMN dispatch_attempts integer NOT NULL DEFAULT 0,
    ADD COLUMN dispatch_claimed_at timestamptz,
    ADD COLUMN dispatch_completed_at timestamptz,
    ADD COLUMN dispatch_error text NOT NULL DEFAULT '';
CREATE INDEX notification_email_verifications_dispatch_idx
    ON public.notification_email_verifications(created_at)
    WHERE dispatch_state IN ('queued', 'sending');
-- +goose StatementEnd

-- +goose Down
DROP INDEX public.notification_email_verifications_dispatch_idx;
ALTER TABLE public.notification_email_verifications
    DROP COLUMN dispatch_state,
    DROP COLUMN dispatch_attempts,
    DROP COLUMN dispatch_claimed_at,
    DROP COLUMN dispatch_completed_at,
    DROP COLUMN dispatch_error;
