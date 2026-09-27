-- +goose Up
CREATE INDEX web_push_subscriptions_profile_page_idx
    ON public.web_push_subscriptions (profile_id, created_at, id);
CREATE INDEX notification_webhooks_profile_page_idx
    ON public.notification_webhooks (profile_id, created_at, id);
CREATE INDEX notification_server_channels_page_idx
    ON public.notification_server_channels (created_at, id);

-- +goose Down
DROP INDEX public.notification_server_channels_page_idx;
DROP INDEX public.notification_webhooks_profile_page_idx;
DROP INDEX public.web_push_subscriptions_profile_page_idx;
