-- +goose NO TRANSACTION

-- +goose Up
-- The artwork revision GC's dormant sweep re-verifies parked revisions. It
-- used to re-stamp every still-referenced row's updated_at once a day and
-- picked rows by an (updated_at, id) index, so each check rewrote the row and
-- every index entry. It now walks parked rows in id order from a persisted
-- cursor and writes only the rows it requeues. after_id = 0 means no cycle is
-- in progress; a new cycle starts once cycle_started_at is a recheck interval
-- old.
CREATE TABLE IF NOT EXISTS public.artwork_revision_gc_dormant_cursor (
    singleton boolean PRIMARY KEY DEFAULT true,
    after_id bigint NOT NULL DEFAULT 0,
    cycle_started_at timestamptz NOT NULL DEFAULT '-infinity',
    updated_at timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT artwork_revision_gc_dormant_cursor_singleton CHECK (singleton),
    CONSTRAINT artwork_revision_gc_dormant_cursor_after_id_check CHECK (after_id >= 0)
);

INSERT INTO public.artwork_revision_gc_dormant_cursor DEFAULT VALUES
ON CONFLICT (singleton) DO NOTHING;

-- Only the old sweep ordered by updated_at.
DROP INDEX CONCURRENTLY IF EXISTS public.artwork_revision_gc_dormant_idx;

-- +goose Down
-- A failed concurrent build can leave an invalid index. Keep retry cleanup
-- outside a transaction so it cannot take a blocking ordinary index-drop lock.
DROP INDEX CONCURRENTLY IF EXISTS public.artwork_revision_gc_dormant_idx;

CREATE INDEX CONCURRENTLY artwork_revision_gc_dormant_idx
    ON public.artwork_revision_gc_candidates (updated_at, id)
    WHERE next_attempt_at IS NULL;

DROP TABLE IF EXISTS public.artwork_revision_gc_dormant_cursor;
