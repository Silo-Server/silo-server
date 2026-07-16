-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.push_devices
    ADD COLUMN fcm_token_ciphertext text,
    ADD COLUMN fcm_token_hash text;

ALTER TABLE public.push_devices
    DROP CONSTRAINT push_devices_platform_check;
ALTER TABLE public.push_devices
    ADD CONSTRAINT push_devices_platform_check CHECK (platform IN ('apple', 'android'));

ALTER TABLE public.push_devices
    DROP CONSTRAINT push_devices_apple_fields_check;
ALTER TABLE public.push_devices
    ADD CONSTRAINT push_devices_platform_fields_check CHECK (
        (
            platform = 'apple'
            AND apns_environment IS NOT NULL
            AND apns_topic IS NOT NULL
            AND apns_token_ciphertext IS NOT NULL
            AND apns_token_hash IS NOT NULL
            AND fcm_token_ciphertext IS NULL
            AND fcm_token_hash IS NULL
        )
        OR (
            platform = 'android'
            AND fcm_token_ciphertext IS NOT NULL
            AND fcm_token_hash IS NOT NULL
            AND apns_environment IS NULL
            AND apns_topic IS NULL
            AND apns_token_ciphertext IS NULL
            AND apns_token_hash IS NULL
        )
    );

CREATE INDEX push_devices_fcm_token_hash_idx
    ON public.push_devices (fcm_token_hash)
    WHERE fcm_token_hash IS NOT NULL;

ALTER TABLE public.push_delivery_attempts
    DROP CONSTRAINT push_delivery_attempts_platform_check;
ALTER TABLE public.push_delivery_attempts
    ADD CONSTRAINT push_delivery_attempts_platform_check CHECK (platform IN ('apple', 'android'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM public.push_delivery_attempts WHERE platform = 'android';
DELETE FROM public.push_devices WHERE platform = 'android';

ALTER TABLE public.push_delivery_attempts
    DROP CONSTRAINT push_delivery_attempts_platform_check;
ALTER TABLE public.push_delivery_attempts
    ADD CONSTRAINT push_delivery_attempts_platform_check CHECK (platform IN ('apple'));

DROP INDEX IF EXISTS push_devices_fcm_token_hash_idx;

ALTER TABLE public.push_devices
    DROP CONSTRAINT push_devices_platform_fields_check;
ALTER TABLE public.push_devices
    ADD CONSTRAINT push_devices_apple_fields_check CHECK (
        platform = 'apple'
        AND apns_environment IS NOT NULL
        AND apns_topic IS NOT NULL
        AND apns_token_ciphertext IS NOT NULL
        AND apns_token_hash IS NOT NULL
    );

ALTER TABLE public.push_devices
    DROP CONSTRAINT push_devices_platform_check;
ALTER TABLE public.push_devices
    ADD CONSTRAINT push_devices_platform_check CHECK (platform IN ('apple'));

ALTER TABLE public.push_devices
    DROP COLUMN fcm_token_ciphertext,
    DROP COLUMN fcm_token_hash;
-- +goose StatementEnd
