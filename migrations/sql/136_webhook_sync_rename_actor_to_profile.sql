-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.webhook_sync_actor_mappings RENAME TO webhook_sync_profile_mappings;

ALTER TABLE public.webhook_sync_profile_mappings
    RENAME COLUMN external_actor_id TO external_user_id;
ALTER TABLE public.webhook_sync_profile_mappings
    RENAME COLUMN external_actor_name TO external_user_name;

ALTER TABLE public.webhook_sync_profile_mappings
    RENAME CONSTRAINT webhook_sync_actor_mappings_connection_actor_unique
    TO webhook_sync_profile_mappings_connection_user_unique;

ALTER INDEX public.idx_webhook_sync_actor_mappings_connection
    RENAME TO idx_webhook_sync_profile_mappings_connection;
ALTER INDEX public.idx_webhook_sync_actor_mappings_profile
    RENAME TO idx_webhook_sync_profile_mappings_profile;

ALTER TABLE public.webhook_sync_item_state
    RENAME COLUMN external_actor_id TO external_user_id;

UPDATE public.webhook_sync_event_logs
SET attrs = (attrs - 'actor_id' - 'actor_name')
            || jsonb_strip_nulls(jsonb_build_object(
                'external_user_id', attrs->'actor_id',
                'external_user_name', attrs->'actor_name'
            ))
WHERE attrs ? 'actor_id' OR attrs ? 'actor_name';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE public.webhook_sync_event_logs
SET attrs = (attrs - 'external_user_id' - 'external_user_name')
            || jsonb_strip_nulls(jsonb_build_object(
                'actor_id', attrs->'external_user_id',
                'actor_name', attrs->'external_user_name'
            ))
WHERE attrs ? 'external_user_id' OR attrs ? 'external_user_name';

ALTER TABLE public.webhook_sync_item_state
    RENAME COLUMN external_user_id TO external_actor_id;

ALTER INDEX public.idx_webhook_sync_profile_mappings_profile
    RENAME TO idx_webhook_sync_actor_mappings_profile;
ALTER INDEX public.idx_webhook_sync_profile_mappings_connection
    RENAME TO idx_webhook_sync_actor_mappings_connection;

ALTER TABLE public.webhook_sync_profile_mappings
    RENAME CONSTRAINT webhook_sync_profile_mappings_connection_user_unique
    TO webhook_sync_actor_mappings_connection_actor_unique;

ALTER TABLE public.webhook_sync_profile_mappings
    RENAME COLUMN external_user_name TO external_actor_name;
ALTER TABLE public.webhook_sync_profile_mappings
    RENAME COLUMN external_user_id TO external_actor_id;

ALTER TABLE public.webhook_sync_profile_mappings RENAME TO webhook_sync_actor_mappings;
-- +goose StatementEnd
