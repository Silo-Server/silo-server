-- +goose Up
-- A claim takes the newest due file, after the files an administrator asked
-- to have made again (available_at = '-infinity'), so the index gives that
-- order. available_at is included so a claim skips rows that are backing
-- off without reading them.
DROP INDEX IF EXISTS public.media_file_trickplay_claim_idx;

CREATE INDEX media_file_trickplay_claim_idx
    ON public.media_file_trickplay ((available_at = '-infinity'::timestamptz) DESC, media_file_id DESC)
    INCLUDE (available_at)
    WHERE state = 'pending';

-- +goose Down
DROP INDEX IF EXISTS public.media_file_trickplay_claim_idx;

CREATE INDEX media_file_trickplay_claim_idx
    ON public.media_file_trickplay (available_at, media_file_id)
    WHERE state = 'pending';
