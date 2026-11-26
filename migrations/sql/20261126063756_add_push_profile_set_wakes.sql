-- +goose Up
-- +goose StatementBegin
-- When each account's phones were last sent a profile set wake. One
-- statement both decides and records a wake (at most one a minute per
-- account), so the limit holds across API servers and does not depend on
-- the account's push device rows, which a device removal deletes.
--
-- No foreign key to users: one would key-share lock the account row on
-- every wake and could deadlock with an account deletion that cascades into
-- the account's devices. A row only matters for a minute; notification
-- retention deletes rows a day old, a deleted account's included.
CREATE TABLE public.push_profile_set_wakes (
    user_id      integer     PRIMARY KEY,
    last_wake_at timestamptz NOT NULL
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.push_profile_set_wakes;
-- +goose StatementEnd
