-- +goose Up
-- +goose StatementBegin
-- Outbound webhooks channel for release notifications
-- (docs/superpowers/plans/notifications/04-outbound-webhooks.md).
--
-- url_ciphertext / signing_secret_ciphertext hold enc:v1: envelopes produced
-- by internal/secret (text, not bytea, matching the repo's at-rest cipher
-- convention). profile_id has no FK: profiles may live in per-user SQLite
-- stores; deletion cleans up in code.
CREATE TABLE public.notification_webhooks (
    id text PRIMARY KEY,
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    name varchar(64) NOT NULL,
    type text NOT NULL,
    url_ciphertext text NOT NULL,
    url_host varchar(253) NOT NULL,
    signing_secret_ciphertext text,
    enabled boolean NOT NULL DEFAULT true,
    notify_favorites boolean NOT NULL DEFAULT true,
    notify_watchlist boolean NOT NULL DEFAULT true,
    notify_continue_watching boolean NOT NULL DEFAULT true,
    notify_next_up boolean NOT NULL DEFAULT true,
    consecutive_failures integer NOT NULL DEFAULT 0,
    disabled_reason varchar(256),
    last_success_at timestamptz,
    last_failure_at timestamptz,
    last_failure_status integer,
    last_failure_message varchar(256),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT notification_webhooks_profile_name_key UNIQUE (profile_id, name),
    CONSTRAINT notification_webhooks_type_check CHECK (type IN ('discord', 'generic')),
    CONSTRAINT notification_webhooks_secret_check CHECK (type = 'discord' OR signing_secret_ciphertext IS NOT NULL)
);

CREATE INDEX notification_webhooks_profile_idx
    ON public.notification_webhooks (profile_id);
CREATE INDEX notification_webhooks_profile_enabled_idx
    ON public.notification_webhooks (profile_id)
    WHERE enabled;

-- Durable dispatch outbox + retry state. `pending` rows are enqueued in the
-- fanout transaction; the post-commit dispatcher claims them, and the retry
-- worker sweeps stale pending rows (crash between commit and dispatch) plus
-- due retries.
CREATE TABLE public.webhook_delivery_attempts (
    id text PRIMARY KEY,
    notification_delivery_id text NOT NULL REFERENCES public.notification_deliveries(id) ON DELETE CASCADE,
    webhook_id text NOT NULL REFERENCES public.notification_webhooks(id) ON DELETE CASCADE,
    attempt_number integer NOT NULL,
    attempted_at timestamptz NOT NULL DEFAULT now(),
    next_retry_at timestamptz,
    http_status integer,
    outcome text NOT NULL,
    failure_message varchar(256),
    CONSTRAINT webhook_delivery_attempts_unique UNIQUE (webhook_id, notification_delivery_id, attempt_number),
    CONSTRAINT webhook_delivery_attempts_outcome_check CHECK (outcome IN ('pending', 'delivered', 'retrying', 'failed', 'auto_disabled'))
);

CREATE INDEX webhook_delivery_attempts_history_idx
    ON public.webhook_delivery_attempts (webhook_id, attempted_at DESC);
CREATE INDEX webhook_delivery_attempts_retry_idx
    ON public.webhook_delivery_attempts (outcome, next_retry_at);
-- Serves the per-delivery claim (ClaimPendingForDelivery) and, critically,
-- the ON DELETE CASCADE from notification_deliveries: without it every
-- retention delete seq-scans this table once per deleted delivery row.
CREATE INDEX webhook_delivery_attempts_delivery_idx
    ON public.webhook_delivery_attempts (notification_delivery_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.webhook_delivery_attempts;
DROP TABLE IF EXISTS public.notification_webhooks;
-- +goose StatementEnd
