-- +goose Up
-- +goose StatementBegin
-- A series can have completed requests still waiting for the library beside a
-- newer open request for other seasons, so a follow belongs to the request
-- that was open when it was made, and a request's notification goes to its
-- own follows. A profile can follow each of a title's requests.
ALTER TABLE public.media_request_follows ADD COLUMN request_id text;

-- A title has one open request at a time, so the request open when a follow
-- was made is the title's latest request created before it, provided that
-- request could still have been open then: still active, or completed no
-- earlier than the follow. A completed request that has not notified also
-- keeps a follow stamped just after its completion when the title has no
-- request since: a completion's timestamp is taken when its transaction
-- begins, so a follow that committed during it can look later, and a deleted
-- request always has a replacement created after the follow. Otherwise that
-- request had closed (a failed request, or one its requester replaced and
-- deleted), and the follow goes to the title's first request since that still
-- has a notification to send, as a new request takes such follows over. With
-- none, it stays with the title's failed request, the one it was made for or
-- else the latest, for the title's next request to take over.
UPDATE public.media_request_follows f
SET request_id = CASE
    WHEN made_for.outcome = 'active'
     AND (made_for.status <> 'completed' OR made_for.completed_at >= f.created_at
          OR (made_for.fulfilled_notified_at IS NULL AND NOT EXISTS (
            SELECT 1 FROM public.media_requests r
            WHERE r.media_type = f.media_type AND r.provider = 'tmdb' AND r.tmdb_id = f.tmdb_id
              AND r.created_at > f.created_at)))
        THEN made_for.id
    ELSE coalesce(
        (SELECT r.id FROM public.media_requests r
         WHERE r.media_type = f.media_type AND r.provider = 'tmdb' AND r.tmdb_id = f.tmdb_id
           AND r.created_at > f.created_at AND r.outcome = 'active'
           AND (r.status <> 'completed' OR r.fulfilled_notified_at IS NULL)
         ORDER BY r.created_at, r.id
         LIMIT 1),
        CASE WHEN made_for.outcome = 'failed' THEN made_for.id END,
        (SELECT r.id FROM public.media_requests r
         WHERE r.media_type = f.media_type AND r.provider = 'tmdb' AND r.tmdb_id = f.tmdb_id
           AND r.outcome = 'failed'
         ORDER BY r.created_at DESC, r.id DESC
         LIMIT 1))
    END
FROM (
    SELECT DISTINCT ON (f2.media_type, f2.tmdb_id, f2.user_id, f2.profile_id)
        f2.media_type, f2.tmdb_id, f2.user_id, f2.profile_id,
        r.id, r.outcome, r.status, r.completed_at, r.fulfilled_notified_at
    FROM public.media_request_follows f2
    LEFT JOIN public.media_requests r
      ON r.media_type = f2.media_type AND r.provider = 'tmdb' AND r.tmdb_id = f2.tmdb_id
     AND r.created_at <= f2.created_at
    ORDER BY f2.media_type, f2.tmdb_id, f2.user_id, f2.profile_id, r.created_at DESC NULLS LAST, r.id DESC
) made_for
WHERE made_for.media_type = f.media_type AND made_for.tmdb_id = f.tmdb_id
  AND made_for.user_id = f.user_id AND made_for.profile_id = f.profile_id;

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
