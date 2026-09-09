-- Explicit compatibility edits can repeat a historical LastPlayedDate while
-- advancing the server write time past a history tombstone. A transaction-local
-- marker distinguishes that supplied event time from legacy writes which omit
-- event_at. Sync cursors and all unmarked writers retain their existing behavior.
-- +goose Up
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.user_watch_progress_stamp() RETURNS trigger AS $stamp$
BEGIN
    NEW.synced_seq := nextval('public.user_watch_progress_seq');
    IF NEW.event_at IS NULL THEN
        NEW.event_at := NEW.updated_at;
    ELSIF TG_OP = 'UPDATE'
        AND current_setting('silo.explicit_progress_event_time', true) IS DISTINCT FROM 'on'
        AND NEW.event_at IS NOT DISTINCT FROM OLD.event_at
        AND NEW.updated_at IS DISTINCT FROM OLD.updated_at THEN
        NEW.event_at := NEW.updated_at;
    END IF;
    RETURN NEW;
END;
$stamp$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.user_watch_progress_stamp() RETURNS trigger AS $stamp$
BEGIN
    NEW.synced_seq := nextval('public.user_watch_progress_seq');
    IF NEW.event_at IS NULL THEN
        NEW.event_at := NEW.updated_at;
    ELSIF TG_OP = 'UPDATE'
        AND NEW.event_at IS NOT DISTINCT FROM OLD.event_at
        AND NEW.updated_at IS DISTINCT FROM OLD.updated_at THEN
        NEW.event_at := NEW.updated_at;
    END IF;
    RETURN NEW;
END;
$stamp$ LANGUAGE plpgsql;
-- +goose StatementEnd
