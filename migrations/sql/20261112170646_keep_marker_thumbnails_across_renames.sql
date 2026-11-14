-- +goose Up
-- A renamed or moved file keeps its row (scanner relinkMovedFile), and its
-- marker images show the same frames: the file's hash, size, modification
-- time, and duration still cover changed bytes, and its links cover a new
-- title. A path change alone no longer clears them. Image identities hash the
-- path, so every scanner write that moves a row to another path sets the
-- column itself: a move within the title rewrites the identities for the new
-- path, and a move to another title clears them (FileRepository
-- RelinkMovedFile, RetitleMovedFile, and ReclaimStandIn).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.invalidate_marker_thumbnails() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF ROW(OLD.marker_segments, OLD.intro_start, OLD.intro_end, OLD.credits_start, OLD.credits_end,
           OLD.recap_start, OLD.recap_end, OLD.preview_start, OLD.preview_end,
           OLD.file_hash, OLD.file_size, OLD.file_modified_at, OLD.duration,
           OLD.content_id, OLD.episode_id, OLD.extra_id, OLD.season_number, OLD.episode_number)
       IS DISTINCT FROM
       ROW(NEW.marker_segments, NEW.intro_start, NEW.intro_end, NEW.credits_start, NEW.credits_end,
           NEW.recap_start, NEW.recap_end, NEW.preview_start, NEW.preview_end,
           NEW.file_hash, NEW.file_size, NEW.file_modified_at, NEW.duration,
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

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION public.invalidate_marker_thumbnails() RETURNS trigger LANGUAGE plpgsql AS $$
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
