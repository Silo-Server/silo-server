-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.user_device_settings RENAME TO user_device_settings_legacy;

INSERT INTO public.user_profiles (
    id,
    user_id,
    name,
    is_primary
)
SELECT
    'default',
    legacy_users.user_id,
    'Default',
    true
FROM (
    SELECT DISTINCT legacy.user_id
    FROM public.user_device_settings_legacy AS legacy
) AS legacy_users
WHERE NOT EXISTS (
    SELECT 1
    FROM public.user_profiles AS profiles
    WHERE profiles.user_id = legacy_users.user_id
);

CREATE TABLE IF NOT EXISTS public.user_device_settings_profile_scoped (
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    device_id text NOT NULL,
    key text NOT NULL,
    value text NOT NULL,
    device_name text NOT NULL DEFAULT '',
    device_platform text NOT NULL DEFAULT '',
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT user_device_settings_profile_scoped_pkey PRIMARY KEY (user_id, profile_id, device_id, key),
    CONSTRAINT user_device_settings_profile_scoped_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE,
    CONSTRAINT user_device_settings_profile_scoped_profile_fkey
        FOREIGN KEY (user_id, profile_id) REFERENCES public.user_profiles(user_id, id) ON DELETE CASCADE
);

INSERT INTO public.user_device_settings_profile_scoped (
    user_id,
    profile_id,
    device_id,
    key,
    value,
    device_name,
    device_platform,
    updated_at
)
WITH target_profiles AS (
    SELECT
        profiles.user_id,
        profiles.id,
        ROW_NUMBER() OVER (
            PARTITION BY profiles.user_id
            ORDER BY profiles.is_primary DESC, profiles.created_at ASC, profiles.id ASC
        ) AS rn
    FROM public.user_profiles AS profiles
)
SELECT
    legacy.user_id,
    target_profiles.id,
    legacy.device_id,
    legacy.key,
    legacy.value,
    legacy.device_name,
    legacy.device_platform,
    legacy.updated_at
FROM public.user_device_settings_legacy AS legacy
JOIN target_profiles
    ON target_profiles.user_id = legacy.user_id
   AND target_profiles.rn = 1;

CREATE INDEX IF NOT EXISTS idx_user_device_settings_profile_scoped_user_key
    ON public.user_device_settings_profile_scoped (user_id, key);

DROP TABLE public.user_device_settings_legacy;

ALTER TABLE public.user_device_settings_profile_scoped RENAME TO user_device_settings;
ALTER INDEX public.idx_user_device_settings_profile_scoped_user_key RENAME TO idx_user_device_settings_user_key;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.user_device_settings RENAME TO user_device_settings_profile_scoped_legacy;

CREATE TABLE IF NOT EXISTS public.user_device_settings_legacy_restore (
    user_id integer NOT NULL,
    device_id text NOT NULL,
    key text NOT NULL,
    value text NOT NULL,
    device_name text NOT NULL DEFAULT '',
    device_platform text NOT NULL DEFAULT '',
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT user_device_settings_legacy_restore_pkey PRIMARY KEY (user_id, device_id, key),
    CONSTRAINT user_device_settings_legacy_restore_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE
);

INSERT INTO public.user_device_settings_legacy_restore (
    user_id,
    device_id,
    key,
    value,
    device_name,
    device_platform,
    updated_at
)
SELECT DISTINCT ON (user_id, device_id, key)
    user_id,
    device_id,
    key,
    value,
    device_name,
    device_platform,
    updated_at
FROM public.user_device_settings_profile_scoped_legacy
ORDER BY user_id, device_id, key, updated_at DESC, profile_id ASC;

CREATE INDEX IF NOT EXISTS idx_user_device_settings_legacy_restore_user_key
    ON public.user_device_settings_legacy_restore (user_id, key);

DROP TABLE public.user_device_settings_profile_scoped_legacy;

ALTER TABLE public.user_device_settings_legacy_restore RENAME TO user_device_settings;
ALTER INDEX public.idx_user_device_settings_legacy_restore_user_key RENAME TO idx_user_device_settings_user_key;
-- +goose StatementEnd
