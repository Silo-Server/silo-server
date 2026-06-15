-- +goose Up
-- +goose StatementBegin
-- request.approved / request.declined notifications: notify the requesting
-- profile when an admin (or auto-approval) resolves their request.
-- At-most-once per (profile, request, type): the operational insert path uses
-- ON CONFLICT DO NOTHING, so an API retry or multi-node race dedupes here
-- instead of double-notifying. Mirrors the request.fulfilled index.
CREATE UNIQUE INDEX notification_deliveries_profile_request_lifecycle_key
    ON public.notification_deliveries (profile_id, (reason_flags->>'request_id'), type)
    WHERE type IN ('request.approved', 'request.declined');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.notification_deliveries_profile_request_lifecycle_key;
-- +goose StatementEnd
