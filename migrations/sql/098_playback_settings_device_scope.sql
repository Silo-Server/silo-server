-- +goose Up
-- +goose StatementBegin
WITH legacy_playback_settings AS (
    SELECT user_id, key, value
    FROM public.user_settings
    WHERE key IN (
        'playback.preferred_quality',
        'playback.audio_language',
        'playback.auto_skip_intro',
        'playback.auto_skip_credits',
        'playback.auto_play_next'
    )
),
known_profile_devices AS (
    SELECT DISTINCT
        user_id,
        profile_id,
        device_id,
        device_name,
        device_platform
    FROM public.user_device_settings
)
INSERT INTO public.user_device_settings (
    user_id,
    profile_id,
    device_id,
    key,
    value,
    device_name,
    device_platform,
    updated_at
)
SELECT
    legacy.user_id,
    device.profile_id,
    device.device_id,
    legacy.key,
    legacy.value,
    device.device_name,
    device.device_platform,
    NOW()
FROM legacy_playback_settings AS legacy
JOIN known_profile_devices AS device
    ON device.user_id = legacy.user_id
ON CONFLICT (user_id, profile_id, device_id, key) DO NOTHING;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
INSERT INTO public.user_settings (
    user_id,
    key,
    value
)
SELECT DISTINCT ON (user_id, key)
    user_id,
    key,
    value
FROM public.user_device_settings
WHERE key IN (
    'playback.preferred_quality',
    'playback.audio_language',
    'playback.auto_skip_intro',
    'playback.auto_skip_credits',
    'playback.auto_play_next'
)
ORDER BY user_id, key, updated_at DESC, profile_id ASC, device_id ASC
ON CONFLICT (user_id, key) DO NOTHING;
-- +goose StatementEnd
