-- +goose Up
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_request_integrations_default_per_kind;
DROP INDEX IF EXISTS idx_request_integrations_default4k_per_kind;
ALTER TABLE public.request_integrations
    DROP CONSTRAINT IF EXISTS request_integrations_kind_check,
    DROP COLUMN IF EXISTS kind,
    DROP COLUMN IF EXISTS root_folder,
    DROP COLUMN IF EXISTS quality_profile_id,
    DROP COLUMN IF EXISTS tags,
    DROP COLUMN IF EXISTS is_4k,
    DROP COLUMN IF EXISTS is_default,
    DROP COLUMN IF EXISTS is_default_4k,
    DROP COLUMN IF EXISTS anime_enabled,
    DROP COLUMN IF EXISTS anime_quality_profile_id,
    DROP COLUMN IF EXISTS anime_root_folder,
    DROP COLUMN IF EXISTS anime_tags,
    DROP COLUMN IF EXISTS options;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.request_integrations
    ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS root_folder text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS quality_profile_id integer,
    ADD COLUMN IF NOT EXISTS tags integer[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS is_4k boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS is_default boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS is_default_4k boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS anime_enabled boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS anime_quality_profile_id integer,
    ADD COLUMN IF NOT EXISTS anime_root_folder text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS anime_tags integer[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS options jsonb NOT NULL DEFAULT '{}'::jsonb;
-- Best-effort backfill from plugin_config (the source of truth).
UPDATE public.request_integrations SET
    kind = COALESCE(plugin_config->>'service_kind', ''),
    root_folder = COALESCE(plugin_config->>'root_folder', ''),
    quality_profile_id = NULLIF(plugin_config->>'quality_profile_id','')::integer,
    is_4k = COALESCE((plugin_config->>'is_4k')::boolean, false),
    is_default = COALESCE((plugin_config->>'is_default')::boolean, false),
    is_default_4k = COALESCE((plugin_config->>'is_default_4k')::boolean, false),
    anime_enabled = COALESCE((plugin_config->>'anime_enabled')::boolean, false),
    anime_root_folder = COALESCE(plugin_config->>'anime_root_folder', '');
-- +goose StatementEnd
