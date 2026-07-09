-- +goose Up
-- +goose StatementBegin
ALTER TABLE autoscan_sources
    ADD COLUMN delivery_mode text NOT NULL DEFAULT 'poll'
        CONSTRAINT autoscan_sources_delivery_mode_check
        CHECK (delivery_mode = ANY (ARRAY['poll'::text, 'webhook'::text]));
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE autoscan_webhook_endpoints (
    source_id          uuid PRIMARY KEY
                       REFERENCES autoscan_sources(id) ON DELETE CASCADE,
    secret_hash        text UNIQUE NOT NULL,
    secret_ref         text NOT NULL,
    secret_suffix      text NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    rotated_at         timestamptz,
    last_received_at   timestamptz,
    last_error_at      timestamptz,
    last_error_message text NOT NULL DEFAULT ''
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE autoscan_events
    ADD COLUMN delivery_mode text NOT NULL DEFAULT 'poll',
    ADD COLUMN provider_event_type text NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE autoscan_events
    DROP COLUMN provider_event_type,
    DROP COLUMN delivery_mode;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE autoscan_webhook_endpoints;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE autoscan_sources
    DROP COLUMN delivery_mode;
-- +goose StatementEnd
