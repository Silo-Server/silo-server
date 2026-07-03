-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.push_delivery_attempts (
    id text PRIMARY KEY,
    notification_delivery_id text REFERENCES public.notification_deliveries(id) ON DELETE CASCADE,
    push_device_id text NOT NULL REFERENCES public.push_devices(id) ON DELETE CASCADE,
    trigger_type text NOT NULL DEFAULT 'delivery',
    provider text NOT NULL,
    platform text NOT NULL,
    attempt_number integer NOT NULL DEFAULT 0,
    attempted_at timestamptz NOT NULL DEFAULT now(),
    next_retry_at timestamptz,
    outcome text NOT NULL DEFAULT 'pending',
    relay_request_id text,
    upstream_status integer,
    upstream_reason varchar(256),
    failure_message varchar(256),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT push_delivery_attempts_trigger_check CHECK (trigger_type IN ('delivery', 'test')),
    CONSTRAINT push_delivery_attempts_delivery_required_check CHECK (
        (trigger_type = 'delivery' AND notification_delivery_id IS NOT NULL)
        OR trigger_type = 'test'
    ),
    CONSTRAINT push_delivery_attempts_provider_check CHECK (provider IN ('silo_relay')),
    CONSTRAINT push_delivery_attempts_platform_check CHECK (platform IN ('apple')),
    CONSTRAINT push_delivery_attempts_outcome_check CHECK (outcome IN ('pending', 'delivered', 'retrying', 'failed'))
);

CREATE UNIQUE INDEX push_delivery_attempts_delivery_unique
    ON public.push_delivery_attempts (push_device_id, notification_delivery_id, attempt_number)
    WHERE notification_delivery_id IS NOT NULL;
CREATE INDEX push_delivery_attempts_delivery_idx
    ON public.push_delivery_attempts (notification_delivery_id)
    WHERE notification_delivery_id IS NOT NULL;
CREATE INDEX push_delivery_attempts_retry_idx
    ON public.push_delivery_attempts (outcome, next_retry_at);
CREATE INDEX push_delivery_attempts_device_history_idx
    ON public.push_delivery_attempts (push_device_id, attempted_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.push_delivery_attempts;
-- +goose StatementEnd
