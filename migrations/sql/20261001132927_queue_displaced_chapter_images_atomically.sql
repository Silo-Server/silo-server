-- +goose Up
-- Queue displaced chapter objects in the transaction that removes their
-- catalog references, including a crash before the worker schedules cleanup.
-- +goose StatementBegin
CREATE FUNCTION public.queue_replaced_chapter_images()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO public.blob_gc_queue (prefix, not_before)
    SELECT DISTINCT chapter->>'thumbnail_path', now() + interval '48 hours'
    FROM jsonb_array_elements(CASE WHEN jsonb_typeof(OLD.chapters) = 'array' THEN OLD.chapters ELSE '[]'::jsonb END) chapter
    WHERE chapter->>'thumbnail_path' LIKE 'chapter-images/' || OLD.id || '/%'
      AND chapter->>'thumbnail_path' ~ '^chapter-images/[1-9][0-9]*/(0|[1-9][0-9]*)(-[a-f0-9]{64})?/w[1-9][0-9]*\.webp$'
      AND NOT EXISTS (
          SELECT 1
          FROM jsonb_array_elements(CASE WHEN jsonb_typeof(NEW.chapters) = 'array' THEN NEW.chapters ELSE '[]'::jsonb END) current_chapter
          WHERE current_chapter->>'thumbnail_path' = chapter->>'thumbnail_path'
      )
    ON CONFLICT (prefix) DO UPDATE
    SET not_before = GREATEST(public.blob_gc_queue.not_before, EXCLUDED.not_before);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER media_files_queue_replaced_chapter_images
AFTER UPDATE OF chapters ON public.media_files
FOR EACH ROW
WHEN (OLD.chapters IS DISTINCT FROM NEW.chapters)
EXECUTE FUNCTION public.queue_replaced_chapter_images();

-- +goose Down
DROP TRIGGER media_files_queue_replaced_chapter_images ON public.media_files;
DROP FUNCTION public.queue_replaced_chapter_images();
