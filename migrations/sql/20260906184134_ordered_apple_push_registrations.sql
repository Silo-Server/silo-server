-- +goose Up
-- +goose StatementBegin
-- Installation ordering survives device/profile cleanup. No APNs token,
-- plaintext installation proof or profile FK belongs in retained metadata.
CREATE TABLE public.apple_push_installations (
    device_id varchar(128) PRIMARY KEY,
    installation_key_hash text NOT NULL DEFAULT '',
    generation bigint NOT NULL DEFAULT 0 CHECK (generation >= 0),
    user_id integer NOT NULL DEFAULT 0,
    profile_id text NOT NULL DEFAULT '',
    intent_hash text NOT NULL DEFAULT '',
    registration_id text NOT NULL DEFAULT '',
    server_device_id text NOT NULL DEFAULT '',
    push_mode text NOT NULL DEFAULT ''
);
-- +goose StatementEnd

-- +goose Down
DROP TABLE public.apple_push_installations;
