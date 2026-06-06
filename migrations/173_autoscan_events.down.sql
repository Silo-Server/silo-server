DROP INDEX IF EXISTS public.idx_scan_runs_autoscan_event;

ALTER TABLE public.scan_runs
    DROP COLUMN IF EXISTS autoscan_event_id;

DROP INDEX IF EXISTS public.idx_autoscan_events_completed;
DROP INDEX IF EXISTS public.idx_autoscan_events_source_completed;
DROP TABLE IF EXISTS public.autoscan_events;
