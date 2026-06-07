-- +goose Up
-- +goose StatementBegin
-- Toggle for hiding an in-progress book from the Continue Listening
-- shelf without affecting the progress row itself. Used by the
-- ABS-compat /me/progress/{itemId}/remove-from-continue-listening +
-- /readd-to-continue-listening endpoints.

ALTER TABLE public.user_watch_progress
    ADD COLUMN IF NOT EXISTS hide_from_continue boolean NOT NULL DEFAULT false;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE public.user_watch_progress
    DROP COLUMN IF EXISTS hide_from_continue;
-- +goose StatementEnd
