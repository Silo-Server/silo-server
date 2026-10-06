-- +goose Up
-- Record the latest URL expiry for the current published revision. Readers
-- update it before returning URLs, and retirement carries it into the queue.
ALTER TABLE public.media_file_trickplay
    ADD COLUMN published_expires_at timestamptz;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.queue_deleted_trickplay_revisions()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO public.blob_gc_queue (prefix, not_before)
    SELECT 'trickplay/' || old_rows.media_file_id || '/' || revisions.revision || '/',
           GREATEST(now() + interval '48 hours', revisions.expires_at)
    FROM old_rows
    CROSS JOIN LATERAL (
        VALUES (old_rows.revision, old_rows.published_expires_at),
               (old_rows.work_revision, NULL::timestamptz)
    ) AS revisions(revision, expires_at)
    WHERE revisions.revision IS NOT NULL
    ON CONFLICT (prefix) DO UPDATE
        SET not_before = GREATEST(public.blob_gc_queue.not_before, EXCLUDED.not_before);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.queue_deleted_trickplay_revisions()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO public.blob_gc_queue (prefix, not_before)
    SELECT 'trickplay/' || old_rows.media_file_id || '/' || revisions.revision || '/', now() + interval '48 hours'
    FROM old_rows
    CROSS JOIN LATERAL (VALUES (old_rows.revision), (old_rows.work_revision)) AS revisions(revision)
    WHERE revisions.revision IS NOT NULL
    ON CONFLICT (prefix) DO NOTHING;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

ALTER TABLE public.media_file_trickplay DROP COLUMN published_expires_at;
