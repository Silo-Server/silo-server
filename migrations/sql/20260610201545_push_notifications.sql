-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.user_devices
    ADD COLUMN push_token     text NULL,
    ADD COLUMN push_transport text NULL,
    ADD COLUMN push_enabled   boolean NOT NULL DEFAULT true,
    ADD COLUMN push_token_at  timestamptz NULL,
    ADD COLUMN push_failures  integer NOT NULL DEFAULT 0;

CREATE TABLE public.push_deliveries (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    notification_id bigint NOT NULL REFERENCES public.notifications(id) ON DELETE CASCADE,
    user_id         integer NOT NULL REFERENCES public.users(id) ON DELETE CASCADE,
    device_id       text NOT NULL,
    transport       text NOT NULL,
    status          text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','sent','failed','skipped','dead')),
    attempts        integer NOT NULL DEFAULT 0,
    not_before      timestamptz NOT NULL DEFAULT now(),
    last_error      text NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX push_deliveries_claim_idx
    ON public.push_deliveries (not_before)
    WHERE status IN ('pending','failed');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE public.push_deliveries;
ALTER TABLE public.user_devices
    DROP COLUMN push_token,
    DROP COLUMN push_transport,
    DROP COLUMN push_enabled,
    DROP COLUMN push_token_at,
    DROP COLUMN push_failures;
-- +goose StatementEnd
