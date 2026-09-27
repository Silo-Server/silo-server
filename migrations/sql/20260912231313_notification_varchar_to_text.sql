-- +goose Up
-- +goose StatementBegin
-- Notification names, device identifiers and delivery diagnostics use text.
-- Application validation and bounded diagnostic writes remain in their owners.
-- This includes the Apple/Android installation stores introduced with API v2.
-- Widening retains rows, defaults, constraints and indexes. ACCESS EXCLUSIVE
-- locks still require a maintenance window even though no heap rewrite is needed.
ALTER TABLE public.notification_server_channels
    ALTER COLUMN name TYPE text,
    ALTER COLUMN url_host TYPE text,
    ALTER COLUMN disabled_reason TYPE text,
    ALTER COLUMN last_failure_message TYPE text;

ALTER TABLE public.notification_webhooks
    ALTER COLUMN name TYPE text,
    ALTER COLUMN url_host TYPE text,
    ALTER COLUMN disabled_reason TYPE text,
    ALTER COLUMN last_failure_message TYPE text;

ALTER TABLE public.push_delivery_attempts
    ALTER COLUMN upstream_reason TYPE text,
    ALTER COLUMN failure_message TYPE text;

ALTER TABLE public.push_devices
    ALTER COLUMN device_id TYPE text;

ALTER TABLE public.web_push_delivery_attempts
    ALTER COLUMN failure_message TYPE text;

ALTER TABLE public.webhook_delivery_attempts
    ALTER COLUMN failure_message TYPE text;

-- device_name has a DEFAULT that should use the widened type. ALTER TYPE
-- leaves the default expression as ''::character varying, which still works but
-- leaves a varchar literal in the catalog of a table that no longer has one.
ALTER TABLE public.web_push_subscriptions
    ALTER COLUMN device_name TYPE text;
ALTER TABLE public.web_push_subscriptions
    ALTER COLUMN device_name SET DEFAULT ''::text;
ALTER TABLE public.apple_push_installations ALTER COLUMN device_id TYPE text;
ALTER TABLE public.android_push_installations ALTER COLUMN device_id TYPE text;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Restoring a cap fails with 22001 if longer values have been stored. The whole
-- Down rolls back; it never truncates data. Operators must resolve such values
-- explicitly before retrying. Do not assume application validation covers direct SQL.
ALTER TABLE public.android_push_installations ALTER COLUMN device_id TYPE character varying(128);
ALTER TABLE public.apple_push_installations ALTER COLUMN device_id TYPE character varying(128);
ALTER TABLE public.web_push_subscriptions
    ALTER COLUMN device_name SET DEFAULT ''::character varying;
ALTER TABLE public.web_push_subscriptions
    ALTER COLUMN device_name TYPE character varying(128);

ALTER TABLE public.webhook_delivery_attempts
    ALTER COLUMN failure_message TYPE character varying(256);

ALTER TABLE public.web_push_delivery_attempts
    ALTER COLUMN failure_message TYPE character varying(256);

ALTER TABLE public.push_devices
    ALTER COLUMN device_id TYPE character varying(128);

ALTER TABLE public.push_delivery_attempts
    ALTER COLUMN upstream_reason TYPE character varying(256),
    ALTER COLUMN failure_message TYPE character varying(256);

ALTER TABLE public.notification_webhooks
    ALTER COLUMN name TYPE character varying(64),
    ALTER COLUMN url_host TYPE character varying(253),
    ALTER COLUMN disabled_reason TYPE character varying(256),
    ALTER COLUMN last_failure_message TYPE character varying(256);

ALTER TABLE public.notification_server_channels
    ALTER COLUMN name TYPE character varying(64),
    ALTER COLUMN url_host TYPE character varying(253),
    ALTER COLUMN disabled_reason TYPE character varying(256),
    ALTER COLUMN last_failure_message TYPE character varying(256);
-- +goose StatementEnd
