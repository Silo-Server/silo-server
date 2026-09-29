-- +goose Up
-- +goose StatementBegin
-- A series can have completed requests still waiting for the library beside a
-- newer open request for other seasons, so a follow belongs to the request
-- that was open when it was made, and a request's notification goes to its
-- own follows. A profile can follow each of a title's requests.
ALTER TABLE public.media_request_follows ADD COLUMN request_id text;

-- A title has one open request at a time, so the request open when a follow
-- was made is the title's latest request created before it. A follow whose
-- request is gone (a failed request its requester replaced) goes to the
-- title's open request, as a new request takes such follows over.
UPDATE public.media_request_follows f
SET request_id = coalesce(
    (SELECT r.id FROM public.media_requests r
     WHERE r.media_type = f.media_type AND r.provider = 'tmdb' AND r.tmdb_id = f.tmdb_id
       AND r.created_at <= f.created_at
     ORDER BY r.created_at DESC, r.id DESC
     LIMIT 1),
    (SELECT r.id FROM public.media_requests r
     WHERE r.media_type = f.media_type AND r.provider = 'tmdb' AND r.tmdb_id = f.tmdb_id
       AND r.outcome = 'active' AND r.status <> 'completed'
     LIMIT 1));
-- A follow no request is left to tell has nothing to wait for.
DELETE FROM public.media_request_follows WHERE request_id IS NULL;

ALTER TABLE public.media_request_follows
    ALTER COLUMN request_id SET NOT NULL,
    ADD CONSTRAINT media_request_follows_request_fkey FOREIGN KEY (request_id)
        REFERENCES public.media_requests (id) ON DELETE CASCADE,
    DROP CONSTRAINT media_request_follows_pkey,
    ADD PRIMARY KEY (user_id, profile_id, request_id);
DROP INDEX public.media_request_follows_profile_idx;
CREATE INDEX media_request_follows_request_idx ON public.media_request_follows (request_id);
CREATE INDEX media_request_follows_title_idx ON public.media_request_follows (media_type, tmdb_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- The title-wide key holds one follow per profile and title; keep the earliest.
DELETE FROM public.media_request_follows f
USING public.media_request_follows keep
WHERE keep.media_type = f.media_type AND keep.tmdb_id = f.tmdb_id
  AND keep.user_id = f.user_id AND keep.profile_id = f.profile_id
  AND (keep.created_at, keep.request_id) < (f.created_at, f.request_id);
DROP INDEX public.media_request_follows_title_idx;
DROP INDEX public.media_request_follows_request_idx;
ALTER TABLE public.media_request_follows
    DROP CONSTRAINT media_request_follows_pkey,
    ADD PRIMARY KEY (media_type, tmdb_id, user_id, profile_id),
    DROP COLUMN request_id;
CREATE INDEX media_request_follows_profile_idx ON public.media_request_follows (user_id, profile_id);
-- +goose StatementEnd
