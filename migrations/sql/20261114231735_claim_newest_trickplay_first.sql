-- +goose NO TRANSACTION

-- +goose Up
-- A claim takes the newest due file, after the files an administrator asked
-- to have made again (available_at = '-infinity'), and this index gives that
-- order. media_file_trickplay_claim_idx stays: when most pending files are
-- backing off, the planner finds the few due ones by available_at instead of
-- walking past the rest here, and servers still claiming in the old order
-- during a rolling upgrade keep their index.
-- A failed concurrent build can leave an invalid index; drop it before retry.
DROP INDEX CONCURRENTLY IF EXISTS public.media_file_trickplay_claim_newest_idx;

CREATE INDEX CONCURRENTLY media_file_trickplay_claim_newest_idx
    ON public.media_file_trickplay ((available_at = '-infinity'::timestamptz) DESC, media_file_id DESC)
    INCLUDE (available_at)
    WHERE state = 'pending';

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS public.media_file_trickplay_claim_newest_idx;
