-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.plugin_repositories
    ADD COLUMN managed_key TEXT,
    ADD COLUMN source_kind TEXT NOT NULL DEFAULT 'external';

ALTER TABLE public.plugin_repositories
    ADD CONSTRAINT plugin_repositories_source_kind_check
    CHECK (source_kind IN ('silo', 'approved_community', 'external'));

CREATE UNIQUE INDEX plugin_repositories_managed_key_unique
    ON public.plugin_repositories (managed_key)
    WHERE managed_key IS NOT NULL;

DO $$
DECLARE
    legacy_instance BOOLEAN :=
        EXISTS (SELECT 1 FROM public.users)
        OR EXISTS (SELECT 1 FROM public.plugin_repositories)
        OR EXISTS (SELECT 1 FROM public.plugin_installations);
    include_community BOOLEAN;
    official_repository_id BIGINT;
    community_repository_id BIGINT;
    migrated_plugin_count INTEGER := 0;
BEGIN
    -- Any persisted account or plugin state is durable evidence that Silo has
    -- run before. Existing servers are opted into the new channel, while a
    -- truly fresh database remains default-off.
    INSERT INTO public.server_settings (key, value)
    VALUES (
        'plugins.include_approved_community_plugins',
        CASE WHEN legacy_instance THEN 'true' ELSE 'false' END
    )
    ON CONFLICT (key) DO NOTHING;

    SELECT LOWER(TRIM(value)) = 'true'
    INTO include_community
    FROM public.server_settings
    WHERE key = 'plugins.include_approved_community_plugins';

    include_community := COALESCE(include_community, false);

    INSERT INTO public.plugin_repositories (
        url,
        enabled,
        display_name,
        managed_key,
        source_kind
    ) VALUES (
        'https://raw.githubusercontent.com/Silo-Server/silo-plugins/main/manifest.json',
        true,
        'Silo maintained',
        'official',
        'silo'
    )
    ON CONFLICT (url) DO UPDATE SET
        enabled = true,
        display_name = EXCLUDED.display_name,
        managed_key = EXCLUDED.managed_key,
        source_kind = EXCLUDED.source_kind,
        updated_at = NOW()
    RETURNING id INTO official_repository_id;

    INSERT INTO public.plugin_repositories (
        url,
        enabled,
        display_name,
        managed_key,
        source_kind
    ) VALUES (
        'https://raw.githubusercontent.com/Silo-Community/silo-plugins/main/manifest.json',
        include_community,
        'Approved community',
        'approved-community',
        'approved_community'
    )
    ON CONFLICT (url) DO UPDATE SET
        enabled = EXCLUDED.enabled,
        display_name = EXCLUDED.display_name,
        managed_key = EXCLUDED.managed_key,
        source_kind = EXCLUDED.source_kind,
        updated_at = NOW()
    RETURNING id INTO community_repository_id;

    -- Preserve the installation row and every dependent configuration/binding
    -- row. Only installations that came from Silo's managed catalog move;
    -- uploads and custom repositories with the same plugin IDs are untouched.
    UPDATE public.plugin_installations
    SET repository_id = community_repository_id,
        available_version = NULL,
        updated_at = NOW()
    WHERE repository_id = official_repository_id
      AND plugin_id IN ('silo.requests.arr', 'silo.requests.seerr');

    GET DIAGNOSTICS migrated_plugin_count = ROW_COUNT;

    INSERT INTO public.server_settings (key, value)
    VALUES ('plugins.approved_community_migrated_plugin_count', migrated_plugin_count::TEXT)
    ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
DECLARE
    official_repository_id BIGINT;
    community_repository_id BIGINT;
BEGIN
    SELECT id INTO official_repository_id
    FROM public.plugin_repositories
    WHERE managed_key = 'official';

    SELECT id INTO community_repository_id
    FROM public.plugin_repositories
    WHERE managed_key = 'approved-community';

    IF official_repository_id IS NOT NULL AND community_repository_id IS NOT NULL THEN
        UPDATE public.plugin_installations
        SET repository_id = official_repository_id,
            available_version = NULL,
            updated_at = NOW()
        WHERE repository_id = community_repository_id
          AND plugin_id IN ('silo.requests.arr', 'silo.requests.seerr');
    END IF;
END $$;

DELETE FROM public.server_settings
WHERE key IN (
    'plugins.include_approved_community_plugins',
    'plugins.approved_community_migrated_plugin_count'
);

UPDATE public.plugin_repositories
SET managed_key = NULL,
    source_kind = 'external',
    updated_at = NOW()
WHERE managed_key IN ('official', 'approved-community');

DROP INDEX IF EXISTS public.plugin_repositories_managed_key_unique;

ALTER TABLE public.plugin_repositories
    DROP CONSTRAINT IF EXISTS plugin_repositories_source_kind_check,
    DROP COLUMN IF EXISTS source_kind,
    DROP COLUMN IF EXISTS managed_key;
-- +goose StatementEnd
