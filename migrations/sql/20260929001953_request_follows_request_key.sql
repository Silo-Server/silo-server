-- +goose Up
-- +goose StatementBegin
-- A series can have completed requests still waiting for the library beside a
-- newer open request for other seasons, so a follow records the request that
-- was open when it was made, and a request's notification goes to its own
-- follows. A follow whose request is gone (a failed request replaced by its
-- requester) has none until the title's next request takes it.
ALTER TABLE public.media_request_follows
    ADD COLUMN request_id text REFERENCES public.media_requests (id) ON DELETE SET NULL;
CREATE INDEX media_request_follows_request_idx ON public.media_request_follows (request_id);

-- Existing follows go to the title's open request, or else to its latest
-- completed request that has not sent its notification.
UPDATE public.media_request_follows f
SET request_id = (
    SELECT r.id FROM public.media_requests r
    WHERE r.media_type = f.media_type AND r.provider = 'tmdb' AND r.tmdb_id = f.tmdb_id
      AND r.outcome = 'active'
      AND (r.status <> 'completed' OR r.fulfilled_notified_at IS NULL)
    ORDER BY r.status = 'completed', r.completed_at DESC NULLS LAST, r.id
    LIMIT 1);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS public.media_request_follows_request_idx;
ALTER TABLE public.media_request_follows DROP COLUMN IF EXISTS request_id;
-- +goose StatementEnd
