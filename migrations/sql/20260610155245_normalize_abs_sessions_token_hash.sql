-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'abs_sessions'
          AND column_name = 'token'
    ) AND NOT EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'abs_sessions'
          AND column_name = 'token_hash'
    ) THEN
        ALTER TABLE public.abs_sessions RENAME COLUMN token TO token_hash;
    ELSIF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'abs_sessions'
          AND column_name = 'token'
    ) THEN
        UPDATE public.abs_sessions
        SET token_hash = token
        WHERE token_hash IS NULL OR token_hash = '';

        ALTER TABLE public.abs_sessions DROP COLUMN token;
    END IF;
END $$;

ALTER TABLE public.abs_sessions
    ALTER COLUMN token_hash SET NOT NULL;

DROP INDEX IF EXISTS public.idx_abs_sessions_token;
CREATE UNIQUE INDEX IF NOT EXISTS idx_abs_sessions_token_hash
    ON public.abs_sessions (token_hash);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.idx_abs_sessions_token_hash;
CREATE UNIQUE INDEX IF NOT EXISTS idx_abs_sessions_token
    ON public.abs_sessions (token_hash);
-- +goose StatementEnd
