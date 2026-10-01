-- +goose Up
-- +goose StatementBegin
-- A profile id is only unique within its account (accounts migrated from
-- before profiles all have a profile named 'default'), and request.fulfilled
-- now reaches followers on other accounts as well as the requester. Key the
-- at-most-once index by account too, so a second account's profile with the
-- same id gets its own delivery instead of deduping against the first.
CREATE UNIQUE INDEX notification_deliveries_account_profile_request_key
    ON public.notification_deliveries (user_id, profile_id, (reason_flags->>'request_id'))
    WHERE type = 'request.fulfilled';
DROP INDEX IF EXISTS public.notification_deliveries_profile_request_key;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- The narrower key cannot hold two accounts' deliveries for one profile id
-- and request; keep the earliest.
DELETE FROM public.notification_deliveries d
USING public.notification_deliveries keep
WHERE d.type = 'request.fulfilled'
  AND keep.type = 'request.fulfilled'
  AND d.profile_id = keep.profile_id
  AND d.reason_flags->>'request_id' = keep.reason_flags->>'request_id'
  AND (keep.created_at, keep.id) < (d.created_at, d.id);

CREATE UNIQUE INDEX notification_deliveries_profile_request_key
    ON public.notification_deliveries (profile_id, (reason_flags->>'request_id'))
    WHERE type = 'request.fulfilled';
DROP INDEX IF EXISTS public.notification_deliveries_account_profile_request_key;
-- +goose StatementEnd
