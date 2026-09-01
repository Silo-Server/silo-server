-- +goose Up
-- +goose StatementBegin
-- Replace the last varchar(n) columns with text.
--
-- Every one of them is in the notifications/push subsystem, which is the only
-- corner of the schema that ever used length-capped strings; the rest of Silo
-- has always used text. The caps are also not the real rule. The application
-- already enforces its own limits before the insert -- destination names are
-- validated to 64 characters in internal/notifications/webhook_service.go,
-- device names truncated to 128 in webpush_service.go, device ids rejected past
-- 128 in push_devices.go, and every failure/disable message is written through
-- SQL `left($n, 256)` -- so the column cap never decides an outcome; it only
-- decides whether a rule that leaks past those guards fails the statement with
-- 22001 instead of storing a long value. Keeping both is duplicated policy that
-- can drift, and the DB copy is the one that cannot be changed without a
-- migration.
--
-- The audit that produced the list, run against a scratch database with the
-- full chain applied:
--
--   SELECT table_name, column_name, character_maximum_length
--   FROM information_schema.columns
--   WHERE table_schema = 'public' AND character_maximum_length IS NOT NULL
--   ORDER BY table_name, ordinal_position;
--
-- It returns exactly these 14 columns and nothing else, so after this migration
-- no varchar(n) column remains anywhere in the schema.
--
-- varchar(n) -> text is binary-coercible: Postgres does not rewrite the table
-- and does not rebuild the indexes (verified on a scratch database by comparing
-- pg_class.relfilenode for push_devices, its primary key and its
-- (profile_id, device_id, platform) unique index before and after -- all three
-- unchanged). The lock is still ACCESS EXCLUSIVE, but it is held for a catalog
-- update, not a copy.
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

-- device_name is the one column of the fourteen with a DEFAULT. ALTER TYPE
-- leaves the default expression as ''::character varying, which still works but
-- leaves a varchar literal in the catalog of a table that no longer has one.
ALTER TABLE public.web_push_subscriptions
    ALTER COLUMN device_name TYPE text;
ALTER TABLE public.web_push_subscriptions
    ALTER COLUMN device_name SET DEFAULT ''::text;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Restore each column's original length, taken from the migration that created
-- it: 20260611120000_notification_webhooks.sql,
-- 20260611150000_web_push_subscriptions.sql,
-- 20260612020209_server_notification_channels.sql,
-- 20260701143000_push_devices.sql and 20260701170000_push_delivery_attempts.sql.
--
-- Lossy in reverse if a value longer than the cap was written while the column
-- was text: the ALTER fails with 22001 rather than truncating. That is the
-- honest behavior -- silently cutting a stored value during a rollback would be
-- worse -- and the application guards above make it unreachable in practice.
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
