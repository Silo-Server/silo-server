CREATE TABLE IF NOT EXISTS public.user_devices (
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    device_id text NOT NULL,
    device_name text NOT NULL DEFAULT '',
    device_platform text NOT NULL DEFAULT '',
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT user_devices_pkey PRIMARY KEY (user_id, profile_id, device_id),
    CONSTRAINT user_devices_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE,
    CONSTRAINT user_devices_profile_fkey
        FOREIGN KEY (user_id, profile_id) REFERENCES public.user_profiles(user_id, id) ON DELETE CASCADE
);

INSERT INTO public.user_devices (
    user_id,
    profile_id,
    device_id,
    device_name,
    device_platform,
    last_seen_at
)
SELECT DISTINCT ON (user_id, profile_id, device_id)
    user_id,
    profile_id,
    device_id,
    COALESCE(device_name, ''),
    COALESCE(device_platform, ''),
    updated_at
FROM (
    SELECT
        settings.user_id,
        COALESCE(valid_profiles.id, fallback_profiles.id) AS profile_id,
        settings.device_id,
        settings.device_name,
        settings.device_platform,
        settings.updated_at
    FROM public.user_device_settings AS settings
    LEFT JOIN public.user_profiles AS valid_profiles
        ON valid_profiles.user_id = settings.user_id
       AND valid_profiles.id = settings.profile_id
    LEFT JOIN LATERAL (
        SELECT profiles.id
        FROM public.user_profiles AS profiles
        WHERE profiles.user_id = settings.user_id
        ORDER BY profiles.is_primary DESC, profiles.created_at ASC, profiles.id ASC
        LIMIT 1
    ) AS fallback_profiles ON true
    WHERE settings.device_id <> ''
) AS resolved_settings
WHERE profile_id IS NOT NULL
ORDER BY user_id, profile_id, device_id, updated_at DESC
ON CONFLICT (user_id, profile_id, device_id) DO UPDATE SET
    device_name = CASE
        WHEN excluded.device_name <> '' THEN excluded.device_name
        ELSE user_devices.device_name
    END,
    device_platform = CASE
        WHEN excluded.device_platform <> '' THEN excluded.device_platform
        ELSE user_devices.device_platform
    END,
    last_seen_at = GREATEST(user_devices.last_seen_at, excluded.last_seen_at);
