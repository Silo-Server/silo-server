-- +goose Up
-- Storage prefixes of derived images (chapter thumbnails today) waiting to be
-- deleted; see internal/blobgc. The collector deletes a prefix once
-- not_before has passed and nothing references it any more, and removes the
-- row only after storage lists the prefix as empty. The check constraint
-- keeps a bug from ever queuing a prefix outside the derived namespaces.
CREATE TABLE public.blob_gc_queue (
    prefix text PRIMARY KEY,
    not_before timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    last_error text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT blob_gc_queue_prefix_check CHECK (prefix ~ '^chapter-images/[1-9][0-9]*/$'),
    CONSTRAINT blob_gc_queue_attempts_check CHECK (attempts >= 0)
);

CREATE INDEX blob_gc_queue_due_idx ON public.blob_gc_queue (not_before);

-- Queue the chapter thumbnails of every deleted media file, whichever path
-- deleted it: missing-file expiry, library removal, or a cascade. Files whose
-- chapters record no thumbnail have nothing stored; the orphan sweep catches
-- objects a failed write left unrecorded.
-- +goose StatementBegin
CREATE FUNCTION public.queue_deleted_media_file_blobs()
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

CREATE TRIGGER media_files_queue_blob_gc
    AFTER DELETE ON public.media_files
    REFERENCING OLD TABLE AS old_rows
    FOR EACH STATEMENT
    EXECUTE FUNCTION public.queue_deleted_media_file_blobs();

-- +goose Down
DROP TRIGGER IF EXISTS media_files_queue_blob_gc ON public.media_files;
DROP FUNCTION IF EXISTS public.queue_deleted_media_file_blobs();
DROP TABLE IF EXISTS public.blob_gc_queue;
