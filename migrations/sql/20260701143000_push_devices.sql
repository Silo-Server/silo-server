-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.push_devices (
    id text PRIMARY KEY,
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    device_id varchar(128) NOT NULL,
    platform text NOT NULL,
    provider text NOT NULL,
    apns_environment text,
    apns_topic text,
    apns_token_ciphertext text,
    apns_token_hash text,
    server_device_id text NOT NULL,
    push_mode text NOT NULL DEFAULT 'private_push',
    enabled boolean NOT NULL DEFAULT true,
    last_seen_at timestamptz,
    last_success_at timestamptz,
    last_failure_at timestamptz,
    last_failure_code text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT push_devices_profile_device_platform_key UNIQUE (profile_id, device_id, platform),
    CONSTRAINT push_devices_server_device_id_key UNIQUE (server_device_id),
    CONSTRAINT push_devices_platform_check CHECK (platform IN ('apple')),
    CONSTRAINT push_devices_provider_check CHECK (provider IN ('silo_relay')),
    CONSTRAINT push_devices_apns_environment_check CHECK (apns_environment IN ('production', 'sandbox')),
    CONSTRAINT push_devices_push_mode_check CHECK (push_mode IN ('off', 'in_app_only', 'private_push')),
    CONSTRAINT push_devices_apple_fields_check CHECK (
        platform = 'apple'
        AND apns_environment IS NOT NULL
        AND apns_topic IS NOT NULL
        AND apns_token_ciphertext IS NOT NULL
        AND apns_token_hash IS NOT NULL
    )
);

CREATE INDEX push_devices_profile_idx ON public.push_devices (profile_id);
CREATE INDEX push_devices_profile_enabled_idx ON public.push_devices (profile_id) WHERE enabled;
CREATE INDEX push_devices_apns_token_hash_idx ON public.push_devices (apns_token_hash);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.push_devices;
-- +goose StatementEnd
