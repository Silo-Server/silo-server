-- +goose Up
-- Profile IDs are unique only within an account (every account from before
-- profiles has a profile "default"), so a webhook name is unique per account
-- profile, not per profile ID. The constraint keeps its name: code maps a
-- violation of it to "a webhook with this name already exists", and older
-- nodes in a rolling upgrade still recognize it.
ALTER TABLE public.notification_webhooks
    DROP CONSTRAINT notification_webhooks_profile_name_key,
    ADD CONSTRAINT notification_webhooks_profile_name_key UNIQUE (user_id, profile_id, name);

-- +goose Down
-- Fails, changing nothing, when two accounts' same-ID profiles now share a
-- webhook name; rename one of them first.
ALTER TABLE public.notification_webhooks
    DROP CONSTRAINT notification_webhooks_profile_name_key,
    ADD CONSTRAINT notification_webhooks_profile_name_key UNIQUE (profile_id, name);
