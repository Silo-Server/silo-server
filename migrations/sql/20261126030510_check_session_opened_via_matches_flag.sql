-- +goose Up
-- The check that keeps auth_sessions.opened_via and via_device_sign_in from
-- disagreeing, after the previous migration brought the stored rows in line.
-- A row a server of the previous release writes has the flag and no
-- opened_via, and passes.
--
-- Adding the check takes an exclusive lock on auth_sessions while it reads
-- the table once: one row per live login, which the daily retention step
-- keeps small. It runs in a transaction of its own, holding no row lock, so
-- it only waits for statements already running and cannot deadlock with them.
--
-- A database that applied the earlier form of the previous migration, which
-- added this check itself, already has it: this then changes nothing.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'auth_sessions_opened_via_matches_flag'
          AND conrelid = 'public.auth_sessions'::regclass
    ) THEN
        ALTER TABLE public.auth_sessions
            ADD CONSTRAINT auth_sessions_opened_via_matches_flag
            CHECK (opened_via IS NULL OR via_device_sign_in IS NULL OR via_device_sign_in = (opened_via = 'device_login'));
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
ALTER TABLE public.auth_sessions
    DROP CONSTRAINT IF EXISTS auth_sessions_opened_via_matches_flag;
