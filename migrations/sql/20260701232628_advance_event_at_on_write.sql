-- Make the watch-progress stamp trigger authoritative for the LWW key.
--
-- The original trigger only defaulted event_at when NULL, so any write path
-- that advanced updated_at without hand-setting event_at (e.g. the batch
-- mark-played path behind jellycompat) left the LWW key stale — a queued
-- offline event older than that write would then win SetProgressIfNewer's
-- event_at comparison and resurrect stale progress. Advance event_at whenever
-- a write changed updated_at but did not explicitly change event_at; writes
-- that DO set event_at (offline sync's clamped client event time) keep their
-- value untouched.

-- +goose Up
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

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.user_watch_progress_stamp() RETURNS trigger AS $stamp$
BEGIN
    NEW.synced_seq := nextval('public.user_watch_progress_seq');
    IF NEW.event_at IS NULL THEN
        NEW.event_at := NEW.updated_at;
    END IF;
    RETURN NEW;
END;
$stamp$ LANGUAGE plpgsql;
-- +goose StatementEnd
