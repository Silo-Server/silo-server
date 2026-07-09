-- +goose Up
-- +goose StatementBegin
CREATE TABLE autoscan_webhook_deliveries (
    id                  bigserial PRIMARY KEY,
    source_id           uuid NOT NULL
                        REFERENCES autoscan_sources(id) ON DELETE CASCADE,
    provider_event_type text NOT NULL,
    changes             jsonb NOT NULL,
    received_at         timestamptz NOT NULL,
    attempt_count       integer NOT NULL DEFAULT 1,
    next_attempt_at     timestamptz NOT NULL DEFAULT now(),
    locked_at           timestamptz,
    locked_by           text NOT NULL DEFAULT '',
    last_error          text NOT NULL DEFAULT '',
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_autoscan_webhook_deliveries_due
    ON autoscan_webhook_deliveries (next_attempt_at, id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE autoscan_webhook_deliveries;
-- +goose StatementEnd
