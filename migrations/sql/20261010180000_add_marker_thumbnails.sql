-- +goose Up
ALTER TABLE public.media_files ADD COLUMN marker_thumbnails jsonb NOT NULL DEFAULT '[]'::jsonb
    CHECK (jsonb_typeof(marker_thumbnails) = 'array');
ALTER TABLE public.blob_gc_queue DROP CONSTRAINT blob_gc_queue_prefix_check;
ALTER TABLE public.blob_gc_queue ADD CONSTRAINT blob_gc_queue_prefix_check CHECK (
    prefix ~ '^chapter-images/[1-9][0-9]*/$'
    OR prefix ~ '^chapter-images/[1-9][0-9]*/((0|[1-9][0-9]*)(-[a-f0-9]{64})?|marker-[a-f0-9]{64}-[a-f0-9]{64})/w[1-9][0-9]*\.webp$'
    OR prefix ~ '^trickplay/[1-9][0-9]*/[1-9][0-9]*/$');

-- +goose StatementBegin
CREATE FUNCTION public.invalidate_marker_thumbnails() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF ROW(OLD.marker_segments, OLD.intro_start, OLD.intro_end, OLD.credits_start, OLD.credits_end,
           OLD.recap_start, OLD.recap_end, OLD.preview_start, OLD.preview_end,
           OLD.file_path, OLD.file_hash, OLD.file_size, OLD.file_modified_at, OLD.duration,
           OLD.content_id, OLD.episode_id, OLD.extra_id, OLD.season_number, OLD.episode_number)
       IS DISTINCT FROM
       ROW(NEW.marker_segments, NEW.intro_start, NEW.intro_end, NEW.credits_start, NEW.credits_end,
           NEW.recap_start, NEW.recap_end, NEW.preview_start, NEW.preview_end,
           NEW.file_path, NEW.file_hash, NEW.file_size, NEW.file_modified_at, NEW.duration,
           NEW.content_id, NEW.episode_id, NEW.extra_id, NEW.season_number, NEW.episode_number) THEN
        NEW.marker_thumbnails := '[]'::jsonb;
    END IF;
    INSERT INTO public.blob_gc_queue (prefix, not_before)
    SELECT DISTINCT image->>'thumbnail_path', now() + interval '48 hours'
    FROM jsonb_array_elements(OLD.marker_thumbnails) image
    WHERE image->>'thumbnail_path' LIKE 'chapter-images/' || OLD.id || '/marker-%'
      AND NOT EXISTS (SELECT 1 FROM jsonb_array_elements(NEW.marker_thumbnails) live
                      WHERE live->>'thumbnail_path' = image->>'thumbnail_path')
    ON CONFLICT (prefix) DO UPDATE SET not_before = GREATEST(public.blob_gc_queue.not_before, EXCLUDED.not_before);
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER media_files_invalidate_marker_thumbnails BEFORE UPDATE ON public.media_files
FOR EACH ROW EXECUTE FUNCTION public.invalidate_marker_thumbnails();

-- A deleted marker-only file has no chapter image, so its prefix must also be
-- queued when it holds marker images.
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
       OR old_rows.marker_thumbnails @? '$[*].thumbnail_path'
       OR protected.until IS NOT NULL
    ON CONFLICT (prefix) DO UPDATE
    SET not_before = GREATEST(public.blob_gc_queue.not_before, EXCLUDED.not_before);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER media_files_invalidate_marker_thumbnails ON public.media_files;
DROP FUNCTION public.invalidate_marker_thumbnails();
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
-- Preserve issued URL deadlines when rolling back the marker namespace.
INSERT INTO public.blob_gc_queue (prefix, not_before)
SELECT 'chapter-images/' || split_part(prefix, '/', 2) || '/', max(not_before)
FROM public.blob_gc_queue WHERE prefix LIKE 'chapter-images/%/marker-%' GROUP BY split_part(prefix, '/', 2)
ON CONFLICT (prefix) DO UPDATE SET not_before = GREATEST(public.blob_gc_queue.not_before, EXCLUDED.not_before);
DELETE FROM public.blob_gc_queue WHERE prefix LIKE 'chapter-images/%/marker-%';
ALTER TABLE public.blob_gc_queue DROP CONSTRAINT blob_gc_queue_prefix_check;
ALTER TABLE public.blob_gc_queue ADD CONSTRAINT blob_gc_queue_prefix_check CHECK (
    prefix ~ '^chapter-images/[1-9][0-9]*/$'
    OR prefix ~ '^chapter-images/[1-9][0-9]*/(0|[1-9][0-9]*)(-[a-f0-9]{64})?/w[1-9][0-9]*\.webp$'
    OR prefix ~ '^trickplay/[1-9][0-9]*/[1-9][0-9]*/$');
ALTER TABLE public.media_files DROP COLUMN marker_thumbnails;
