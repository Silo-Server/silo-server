-- +goose Up
-- +goose StatementBegin
-- Web Push channel for release notifications. Browser PushSubscriptions are
-- profile-scoped; payloads are end-to-end encrypted (RFC 8291) so the
-- browser vendor's push service never sees notification content. profile_id
-- has no FK: profiles may live in per-user SQLite stores; deletion cleans up
-- in code.
CREATE TABLE public.web_push_subscriptions (
    id text PRIMARY KEY,
    user_id integer NOT NULL,
    profile_id text NOT NULL,
    -- The push-service URL is unique per browser registration. A
    -- resubscription from the same browser under a different profile
    -- reassigns the row (one endpoint notifies exactly one profile).
    endpoint text NOT NULL,
    p256dh text NOT NULL,
    auth text NOT NULL,
    device_name varchar(128) NOT NULL DEFAULT '',
    enabled boolean NOT NULL DEFAULT true,
    consecutive_failures integer NOT NULL DEFAULT 0,
    last_success_at timestamptz,
    last_failure_at timestamptz,
    last_failure_status integer,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT web_push_subscriptions_endpoint_key UNIQUE (endpoint)
);

CREATE INDEX web_push_subscriptions_profile_idx
    ON public.web_push_subscriptions (profile_id);
CREATE INDEX web_push_subscriptions_profile_enabled_idx
    ON public.web_push_subscriptions (profile_id)
    WHERE enabled;

-- Durable dispatch outbox + retry state, mirroring webhook_delivery_attempts:
-- `pending` rows are enqueued in the fanout transaction, claimed post-commit,
-- and swept by the retry worker after a crash.
CREATE TABLE public.web_push_delivery_attempts (
    id text PRIMARY KEY,
    notification_delivery_id text NOT NULL REFERENCES public.notification_deliveries(id) ON DELETE CASCADE,
    subscription_id text NOT NULL REFERENCES public.web_push_subscriptions(id) ON DELETE CASCADE,
    attempt_number integer NOT NULL,
    attempted_at timestamptz NOT NULL DEFAULT now(),
    next_retry_at timestamptz,
    http_status integer,
    outcome text NOT NULL,
    failure_message varchar(256),
    CONSTRAINT web_push_delivery_attempts_unique UNIQUE (subscription_id, notification_delivery_id, attempt_number),
    CONSTRAINT web_push_delivery_attempts_outcome_check CHECK (outcome IN ('pending', 'delivered', 'retrying', 'failed'))
);

CREATE INDEX web_push_delivery_attempts_retry_idx
    ON public.web_push_delivery_attempts (outcome, next_retry_at);
-- Serves the per-delivery claim (ClaimPendingForDelivery) and, critically,
-- the ON DELETE CASCADE from notification_deliveries: without it every
-- retention delete seq-scans this table once per deleted delivery row.
CREATE INDEX web_push_delivery_attempts_delivery_idx
    ON public.web_push_delivery_attempts (notification_delivery_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.web_push_delivery_attempts;
DROP TABLE IF EXISTS public.web_push_subscriptions;
-- +goose StatementEnd
