-- +goose Up
-- A chapter thumbnail a newer one replaced, after a preview width change, is
-- queued by its own key and deleted once no chapter references it.
ALTER TABLE public.blob_gc_queue DROP CONSTRAINT blob_gc_queue_prefix_check;
ALTER TABLE public.blob_gc_queue ADD CONSTRAINT blob_gc_queue_prefix_check
    CHECK (prefix ~ '^chapter-images/[1-9][0-9]*/$'
        OR prefix ~ '^chapter-images/[1-9][0-9]*/(0|[1-9][0-9]*)/w[1-9][0-9]*\.webp$'
        OR prefix ~ '^trickplay/[1-9][0-9]*/[1-9][0-9]*/$');

-- +goose Down
DELETE FROM public.blob_gc_queue WHERE prefix ~ '^chapter-images/.*\.webp$';
ALTER TABLE public.blob_gc_queue DROP CONSTRAINT blob_gc_queue_prefix_check;
ALTER TABLE public.blob_gc_queue ADD CONSTRAINT blob_gc_queue_prefix_check
    CHECK (prefix ~ '^chapter-images/[1-9][0-9]*/$' OR prefix ~ '^trickplay/[1-9][0-9]*/[1-9][0-9]*/$');
