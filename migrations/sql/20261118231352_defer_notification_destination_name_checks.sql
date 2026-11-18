-- +goose Up
-- A webhook's or plugin destination's name is unique per account profile.
-- Restoring a backup carries the live rows back by id in one upsert, so two
-- destinations whose names were swapped since the backup would collide
-- part way through it and abort the restore. DEFERRABLE (still initially
-- immediate) moves the check to the end of each statement, after every row
-- of the upsert is written; nothing defers it further. The constraints keep
-- their names, which code maps to "a destination with this name already
-- exists". No ON CONFLICT names either constraint, which a deferrable one
-- can't serve.
ALTER TABLE public.notification_webhooks
    DROP CONSTRAINT notification_webhooks_profile_name_key,
    ADD CONSTRAINT notification_webhooks_profile_name_key UNIQUE (user_id, profile_id, name) DEFERRABLE INITIALLY IMMEDIATE;

ALTER TABLE public.notification_plugin_destinations
    DROP CONSTRAINT notification_plugin_destinations_name_key,
    ADD CONSTRAINT notification_plugin_destinations_name_key UNIQUE (user_id, profile_id, name) DEFERRABLE INITIALLY IMMEDIATE;

-- +goose Down
ALTER TABLE public.notification_webhooks
    DROP CONSTRAINT notification_webhooks_profile_name_key,
    ADD CONSTRAINT notification_webhooks_profile_name_key UNIQUE (user_id, profile_id, name);

ALTER TABLE public.notification_plugin_destinations
    DROP CONSTRAINT notification_plugin_destinations_name_key,
    ADD CONSTRAINT notification_plugin_destinations_name_key UNIQUE (user_id, profile_id, name);
