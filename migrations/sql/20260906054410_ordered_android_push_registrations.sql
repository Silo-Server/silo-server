-- +goose Up
-- +goose StatementBegin
-- Installation authority and the last accepted command survive device-row and
-- profile deletion. No token, plaintext installation key, or profile FK here.
CREATE TABLE public.android_push_installations (
    device_id varchar(128) PRIMARY KEY,
    installation_key_hash text NOT NULL DEFAULT '',
    generation bigint NOT NULL DEFAULT 0 CHECK (generation >= 0),
    user_id integer NOT NULL DEFAULT 0,
    profile_id text NOT NULL DEFAULT '',
    intent_hash text NOT NULL DEFAULT '',
    registration_id text NOT NULL DEFAULT '',
    server_device_id text NOT NULL DEFAULT '',
    push_mode text NOT NULL DEFAULT '',
    removed boolean NOT NULL DEFAULT false
);
-- +goose StatementEnd

-- +goose Down
DROP TABLE public.android_push_installations;
