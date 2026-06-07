-- +goose Up
-- +goose StatementBegin
ALTER TABLE public.autoscan_events
    DROP CONSTRAINT IF EXISTS autoscan_events_status_check;

ALTER TABLE public.autoscan_events
    ADD CONSTRAINT autoscan_events_status_check
    CHECK (status = ANY (ARRAY[
        'running'::text,
        'success'::text,
        'error'::text,
        'unresolved'::text
    ]));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE public.autoscan_events
SET status = 'error',
    error_message = CASE
        WHEN error_message = '' THEN 'poll started but did not finish'
        ELSE error_message
    END
WHERE status = 'running';

ALTER TABLE public.autoscan_events
    DROP CONSTRAINT IF EXISTS autoscan_events_status_check;

ALTER TABLE public.autoscan_events
    ADD CONSTRAINT autoscan_events_status_check
    CHECK (status = ANY (ARRAY[
        'success'::text,
        'error'::text,
        'unresolved'::text
    ]));
-- +goose StatementEnd
