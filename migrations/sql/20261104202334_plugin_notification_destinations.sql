-- +goose Up
-- +goose StatementBegin
-- Profile notification destinations provided by notification_channel.v1
-- plugins (docs/architecture/notifications.md, "Plugin destinations").
--
-- A destination names its kind by plugin_id and capability_id, which stay
-- stable across plugin upgrades and reinstalls. config_ciphertext is an
-- enc:v1: envelope of the destination's settings JSON, bound to the row id;
-- secret fields in it never leave the server. profile_id has no FK, as for
-- notification_webhooks: deletion cleans up in code.
CREATE TABLE public.notification_plugin_destinations (
    id text PRIMARY KEY,
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    plugin_id text NOT NULL,
    capability_id text NOT NULL,
    name text NOT NULL,
    config_ciphertext text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    notify_favorites boolean NOT NULL DEFAULT true,
    notify_watchlist boolean NOT NULL DEFAULT true,
    notify_continue_watching boolean NOT NULL DEFAULT true,
    notify_next_up boolean NOT NULL DEFAULT true,
    notify_requests boolean NOT NULL DEFAULT true,
    consecutive_failures integer NOT NULL DEFAULT 0,
    disabled_reason text,
    last_success_at timestamptz,
    last_failure_at timestamptz,
    last_failure_message text,
    revision bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT notification_plugin_destinations_name_key UNIQUE (user_id, profile_id, name)
);

CREATE INDEX notification_plugin_destinations_profile_enabled_idx
    ON public.notification_plugin_destinations (user_id, profile_id)
    WHERE enabled;

-- Durable dispatch outbox and retry log, the same shape and lifecycle as
-- webhook_delivery_attempts: pending rows are enqueued in the delivery's
-- transaction, claimed with a lease, and swept for retries.
CREATE TABLE public.plugin_destination_delivery_attempts (
    id text PRIMARY KEY,
    notification_delivery_id text NOT NULL REFERENCES public.notification_deliveries(id) ON DELETE CASCADE,
    destination_id text NOT NULL REFERENCES public.notification_plugin_destinations(id) ON DELETE CASCADE,
    attempt_number integer NOT NULL,
    attempted_at timestamptz NOT NULL DEFAULT now(),
    next_retry_at timestamptz,
    outcome text NOT NULL,
    failure_message text,
    CONSTRAINT plugin_destination_delivery_attempts_unique UNIQUE (destination_id, notification_delivery_id, attempt_number),
    CONSTRAINT plugin_destination_delivery_attempts_outcome_check CHECK (outcome IN ('pending', 'delivered', 'retrying', 'failed', 'auto_disabled'))
);

CREATE INDEX plugin_destination_delivery_attempts_retry_idx
    ON public.plugin_destination_delivery_attempts (outcome, next_retry_at);
-- Serves the per-delivery claim and the ON DELETE CASCADE from
-- notification_deliveries retention.
CREATE INDEX plugin_destination_delivery_attempts_delivery_idx
    ON public.plugin_destination_delivery_attempts (notification_delivery_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.plugin_destination_delivery_attempts;
DROP TABLE IF EXISTS public.notification_plugin_destinations;
-- +goose StatementEnd
