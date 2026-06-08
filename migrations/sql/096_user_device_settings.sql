-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS public.user_device_settings (
    user_id integer NOT NULL,
    device_id text NOT NULL,
    key text NOT NULL,
    value text NOT NULL,
    device_name text NOT NULL DEFAULT '',
    device_platform text NOT NULL DEFAULT '',
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT user_device_settings_pkey PRIMARY KEY (user_id, device_id, key),
    CONSTRAINT user_device_settings_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_user_device_settings_user_key
    ON public.user_device_settings (user_id, key);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS public.user_device_settings;
-- +goose StatementEnd
