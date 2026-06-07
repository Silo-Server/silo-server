-- +goose Up
-- +goose StatementBegin
UPDATE public.task_triggers
SET interval = 60 * 60 * 1000
WHERE task_key = 'sync_watch_providers'
  AND type = 'interval'
  AND interval = 15 * 60 * 1000;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE public.task_triggers
SET interval = 15 * 60 * 1000
WHERE task_key = 'sync_watch_providers'
  AND type = 'interval'
  AND interval = 60 * 60 * 1000;
-- +goose StatementEnd
