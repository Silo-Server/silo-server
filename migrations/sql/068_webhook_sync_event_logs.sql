-- +goose Up
-- +goose StatementBegin
CREATE TABLE public.webhook_sync_event_logs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    connection_id uuid NOT NULL REFERENCES public.webhook_sync_connections(id) ON DELETE CASCADE,
    received_at timestamptz NOT NULL DEFAULT now(),
    request_id text NOT NULL DEFAULT '',
    http_status integer NOT NULL,
    outcome text NOT NULL,
    summary text NOT NULL DEFAULT '',
    error_message text NOT NULL DEFAULT '',
    body_excerpt text NOT NULL DEFAULT '',
    attrs jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX idx_webhook_sync_event_logs_connection_received
    ON public.webhook_sync_event_logs (connection_id, received_at DESC, id DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.webhook_sync_event_logs;
-- +goose StatementEnd
