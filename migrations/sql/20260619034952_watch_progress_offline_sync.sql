-- Offline progress reconciliation (downloads v2, Phase 4): give user_watch_progress
-- two server-owned facets so cross-device delta delivery never depends on a client
-- clock. See docs/superpowers/specs/2026-06-18-offline-sync-mobile-design.md
-- ("Progress reconciliation" / invariant 1).
--   * event_at   — the LWW comparison key (the client event time, clamped on ingest).
--   * synced_seq — a server-assigned monotonic cursor, stamped on EVERY write by a
--                  trigger so it can never be influenced by the client.

-- +goose Up
-- +goose StatementBegin
CREATE SEQUENCE IF NOT EXISTS public.user_watch_progress_seq;

ALTER TABLE public.user_watch_progress
    ADD COLUMN IF NOT EXISTS event_at   timestamptz,
    ADD COLUMN IF NOT EXISTS synced_seq bigint;

-- Backfill: the LWW key starts as the existing write time; assign monotonic
-- sequence numbers in write-time order so existing deltas are well-ordered.
UPDATE public.user_watch_progress SET event_at = updated_at WHERE event_at IS NULL;

WITH ordered AS (
    SELECT ctid, row_number() OVER (ORDER BY updated_at, ctid) AS rn
    FROM public.user_watch_progress
)
UPDATE public.user_watch_progress p
SET synced_seq = o.rn
FROM ordered o
WHERE p.ctid = o.ctid AND p.synced_seq IS NULL;

SELECT setval('public.user_watch_progress_seq',
              GREATEST(COALESCE((SELECT MAX(synced_seq) FROM public.user_watch_progress), 0), 1));

-- Stamp synced_seq on every write; default event_at to the write time when a
-- caller (online write) did not supply a client event time.
CREATE OR REPLACE FUNCTION public.user_watch_progress_stamp() RETURNS trigger AS $stamp$
BEGIN
    NEW.synced_seq := nextval('public.user_watch_progress_seq');
    IF NEW.event_at IS NULL THEN
        NEW.event_at := NEW.updated_at;
    END IF;
    RETURN NEW;
END;
$stamp$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS user_watch_progress_stamp_trg ON public.user_watch_progress;
CREATE TRIGGER user_watch_progress_stamp_trg
    BEFORE INSERT OR UPDATE ON public.user_watch_progress
    FOR EACH ROW EXECUTE FUNCTION public.user_watch_progress_stamp();

CREATE INDEX IF NOT EXISTS user_watch_progress_synced_idx
    ON public.user_watch_progress (user_id, profile_id, synced_seq);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS user_watch_progress_stamp_trg ON public.user_watch_progress;
DROP FUNCTION IF EXISTS public.user_watch_progress_stamp();
DROP INDEX IF EXISTS public.user_watch_progress_synced_idx;
ALTER TABLE public.user_watch_progress DROP COLUMN IF EXISTS synced_seq, DROP COLUMN IF EXISTS event_at;
DROP SEQUENCE IF EXISTS public.user_watch_progress_seq;
-- +goose StatementEnd
