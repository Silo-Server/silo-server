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
FROM public.user_device_settings
WHERE device_id <> ''
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
