-- +goose Up
-- +goose StatementBegin
-- The client address of the request that enrolled a key, as the login
-- session that request opened records it (auth_sessions.ip_address). A key
-- enrolled before this column existed, or by a request whose address the
-- server could not tell, has none.
ALTER TABLE public.auth_person_keys ADD COLUMN enrolled_ip_address inet;
ALTER TABLE public.auth_device_keys ADD COLUMN enrolled_ip_address inet;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.auth_device_keys DROP COLUMN IF EXISTS enrolled_ip_address;
ALTER TABLE public.auth_person_keys DROP COLUMN IF EXISTS enrolled_ip_address;
-- +goose StatementEnd
