-- +goose Up
-- +goose StatementBegin
-- The login session that registered the device. Push stops once that session
-- has ended. There is no foreign key: backups carry push_devices but leave
-- auth_sessions out, so a restored row can name a session that no longer
-- exists, which counts as ended. NULL is a registration with no known session
-- (an API key, or one made before this column), which keeps delivering.
ALTER TABLE public.push_devices ADD COLUMN login_session_id text;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.push_devices DROP COLUMN IF EXISTS login_session_id;
-- +goose StatementEnd
