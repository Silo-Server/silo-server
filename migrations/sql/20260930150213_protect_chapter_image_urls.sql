-- +goose Up
-- Immutable chapter images include the encoded image hash in the chapter
-- directory. Older numeric directories remain valid during the transition.
ALTER TABLE public.blob_gc_queue DROP CONSTRAINT blob_gc_queue_prefix_check;
ALTER TABLE public.blob_gc_queue ADD CONSTRAINT blob_gc_queue_prefix_check
    CHECK (prefix ~ '^chapter-images/[1-9][0-9]*/$'
        OR prefix ~ '^chapter-images/[1-9][0-9]*/(0|[1-9][0-9]*)(-[a-f0-9]{64})?/w[1-9][0-9]*\.webp$'
        OR prefix ~ '^trickplay/[1-9][0-9]*/[1-9][0-9]*/$');

-- File deletion must retain all images until the last URL issued before it
-- expires, including displaced widths protected by a different API replica.
CREATE INDEX blob_gc_queue_chapter_file_idx
    ON public.blob_gc_queue ((split_part(prefix, '/', 2)))
    WHERE prefix LIKE 'chapter-images/%';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.queue_deleted_media_file_blobs()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO public.blob_gc_queue (prefix, not_before)
    SELECT 'chapter-images/' || old_rows.id || '/',
           GREATEST(now() + interval '24 hours', protected.until)
    FROM old_rows
    CROSS JOIN LATERAL (
        SELECT max(q.not_before) AS until
        FROM public.blob_gc_queue q
        WHERE q.prefix LIKE 'chapter-images/%'
          AND split_part(q.prefix, '/', 2) = old_rows.id::text
    ) protected
    WHERE old_rows.chapters @? '$[*].thumbnail_path'
       OR protected.until IS NOT NULL
    ON CONFLICT (prefix) DO UPDATE
    SET not_before = GREATEST(public.blob_gc_queue.not_before, EXCLUDED.not_before);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- An older collector does not understand immutable child keys. Transfer
-- their protection to the file prefix before narrowing the queue constraint.
INSERT INTO public.blob_gc_queue (prefix, not_before)
SELECT 'chapter-images/' || split_part(prefix, '/', 2) || '/', max(not_before)
FROM public.blob_gc_queue
WHERE prefix ~ '^chapter-images/[1-9][0-9]*/(0|[1-9][0-9]*)-[a-f0-9]{64}/w[1-9][0-9]*\.webp$'
GROUP BY split_part(prefix, '/', 2)
ON CONFLICT (prefix) DO UPDATE
SET not_before = GREATEST(public.blob_gc_queue.not_before, EXCLUDED.not_before);

DELETE FROM public.blob_gc_queue
WHERE prefix ~ '^chapter-images/[1-9][0-9]*/(0|[1-9][0-9]*)-[a-f0-9]{64}/w[1-9][0-9]*\.webp$';

ALTER TABLE public.blob_gc_queue DROP CONSTRAINT blob_gc_queue_prefix_check;
ALTER TABLE public.blob_gc_queue ADD CONSTRAINT blob_gc_queue_prefix_check
    CHECK (prefix ~ '^chapter-images/[1-9][0-9]*/$'
        OR prefix ~ '^chapter-images/[1-9][0-9]*/(0|[1-9][0-9]*)/w[1-9][0-9]*\.webp$'
        OR prefix ~ '^trickplay/[1-9][0-9]*/[1-9][0-9]*/$');

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.queue_deleted_media_file_blobs()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO public.blob_gc_queue (prefix, not_before)
    SELECT 'chapter-images/' || old_rows.id || '/', now() + interval '24 hours'
    FROM old_rows
    WHERE old_rows.chapters @? '$[*].thumbnail_path'
    ON CONFLICT (prefix) DO NOTHING;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

DROP INDEX public.blob_gc_queue_chapter_file_idx;
