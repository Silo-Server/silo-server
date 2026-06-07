-- +goose Up
-- +goose StatementBegin
-- NOTE: existing rows get installation_id = NULL because the plugin install id is
-- not known at migration time. Such connections are treated as "not bound to a
-- plugin installation" and are skipped during fulfillment until an admin re-saves
-- the connection to bind it to an installed request_router plugin.
ALTER TABLE public.request_integrations
    ADD COLUMN IF NOT EXISTS capability_id text NOT NULL DEFAULT 'request_router.v1',
    ADD COLUMN IF NOT EXISTS installation_id integer,
    ADD COLUMN IF NOT EXISTS supported_media_types text[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS plugin_config jsonb NOT NULL DEFAULT '{}'::jsonb;

-- Fold the arr-specific typed columns into the generic plugin_config blob.
-- service_kind comes from the old `kind`; supported_media_types is derived from it.
UPDATE public.request_integrations
SET plugin_config = jsonb_strip_nulls(
        coalesce(options, '{}'::jsonb) || jsonb_build_object(
            'service_kind', kind,
            'root_folder', root_folder,
            'quality_profile_id', quality_profile_id,
            'tags', to_jsonb(tags),
            'is_4k', is_4k,
            'is_default', is_default,
            'is_default_4k', is_default_4k,
            'anime_enabled', anime_enabled,
            'anime_quality_profile_id', anime_quality_profile_id,
            'anime_root_folder', anime_root_folder,
            'anime_tags', to_jsonb(anime_tags)
        )),
    supported_media_types = CASE WHEN kind = 'sonarr' THEN ARRAY['series'] ELSE ARRAY['movie'] END
WHERE plugin_config = '{}'::jsonb;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.request_integrations
    DROP COLUMN IF EXISTS capability_id,
    DROP COLUMN IF EXISTS installation_id,
    DROP COLUMN IF EXISTS supported_media_types,
    DROP COLUMN IF EXISTS plugin_config;
-- +goose StatementEnd
