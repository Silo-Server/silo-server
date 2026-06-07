-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.user_watch_history
    ADD COLUMN watch_identity JSONB NOT NULL DEFAULT '{}'::jsonb;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.user_watch_history DROP COLUMN watch_identity;
-- +goose StatementEnd
