-- +goose Up
-- Embedded subtitle tracks read whole and kept in the assets blob store, so
-- later requests convert the small kept stream instead of reading the source
-- again; see docs/architecture/embedded-subtitle-index.md. A row records the
-- source version it was read from (source_size, source_mtime_ns); a reader
-- treats a row for another version as absent, and a catalog update that
-- changes the file's size or modification time deletes the file's rows.
CREATE TABLE public.embedded_subtitle_tracks (
    media_file_id integer NOT NULL REFERENCES public.media_files(id) ON DELETE CASCADE,
    embedded_stream_index integer NOT NULL,
    container_track_id text NOT NULL,
    codec text NOT NULL,
    source_size bigint NOT NULL,
    source_mtime_ns bigint NOT NULL,
    -- The kept stream: what FFmpeg reads with -f stream_format, after
    -- subtracting start_time_us like a demux of the source would.
    blob_key text NOT NULL,
    stream_format text NOT NULL,
    start_time_us bigint NOT NULL,
    content_sha256 text NOT NULL,
    byte_size bigint NOT NULL,
    packet_count integer NOT NULL,
    completeness text NOT NULL,
    method text NOT NULL,
    -- Whether a text track's cues carry ASS override tags such as {\an8};
    -- NULL when not looked for (ASS and bitmap tracks).
    has_override_tags boolean,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (media_file_id, embedded_stream_index),
    CONSTRAINT embedded_subtitle_tracks_stream_index_check CHECK (embedded_stream_index >= 0),
    CONSTRAINT embedded_subtitle_tracks_format_check CHECK (stream_format IN ('matroska', 'mp4')),
    CONSTRAINT embedded_subtitle_tracks_method_check CHECK (method IN ('cues', 'ffmpeg')),
    CONSTRAINT embedded_subtitle_tracks_completeness_check CHECK (completeness IN ('complete', 'unknown')),
    CONSTRAINT embedded_subtitle_tracks_sha256_check CHECK (content_sha256 ~ '^[a-f0-9]{64}$'),
    CONSTRAINT embedded_subtitle_tracks_counts_check CHECK (byte_size > 0 AND packet_count > 0 AND source_size >= 0),
    -- Keys name immutable bytes, in the shape blob_gc_queue admits.
    CONSTRAINT embedded_subtitle_tracks_blob_key_check CHECK (blob_key =
        'subtitles/embedded/' || media_file_id || '/' || embedded_stream_index || '-' || content_sha256 ||
        CASE stream_format WHEN 'mp4' THEN '.mp4' ELSE '.mks' END)
);

-- A server reading a track whole holds its claim, so servers do not read the
-- same track at once. A claim only saves work: a read that outlives its lease
-- still keeps an equally valid track.
CREATE TABLE public.embedded_subtitle_track_claims (
    media_file_id integer NOT NULL REFERENCES public.media_files(id) ON DELETE CASCADE,
    embedded_stream_index integer NOT NULL,
    claim_token uuid NOT NULL,
    lease_expires_at timestamptz NOT NULL,
    PRIMARY KEY (media_file_id, embedded_stream_index)
);

-- Kept streams are queued by key when their row goes, whichever path removes
-- it (a replacement, a changed source, a deleted media file), and by file
-- prefix when the orphan sweep finds objects of a deleted file.
ALTER TABLE public.blob_gc_queue DROP CONSTRAINT blob_gc_queue_prefix_check;
ALTER TABLE public.blob_gc_queue ADD CONSTRAINT blob_gc_queue_prefix_check CHECK (
    prefix ~ '^chapter-images/[1-9][0-9]*/$'
    OR prefix ~ '^chapter-images/[1-9][0-9]*/((0|[1-9][0-9]*)(-[a-f0-9]{64})?|marker-[a-f0-9]{64}-[a-f0-9]{64})/w[1-9][0-9]*\.webp$'
    OR prefix ~ '^trickplay/[1-9][0-9]*/[1-9][0-9]*/$'
    OR prefix ~ '^subtitles/embedded/[1-9][0-9]*/$'
    OR prefix ~ '^subtitles/embedded/[1-9][0-9]*/(0|[1-9][0-9]*)-[a-f0-9]{64}\.(mks|mp4)$');

-- +goose StatementBegin
CREATE FUNCTION public.queue_removed_embedded_subtitle_tracks()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        INSERT INTO public.blob_gc_queue (prefix, not_before)
        SELECT old_rows.blob_key, now() + interval '24 hours'
        FROM old_rows
        ON CONFLICT (prefix) DO UPDATE
        SET not_before = GREATEST(public.blob_gc_queue.not_before, EXCLUDED.not_before);
    ELSE
        INSERT INTO public.blob_gc_queue (prefix, not_before)
        SELECT old_rows.blob_key, now() + interval '24 hours'
        FROM old_rows
        JOIN new_rows USING (media_file_id, embedded_stream_index)
        WHERE old_rows.blob_key <> new_rows.blob_key
        ON CONFLICT (prefix) DO UPDATE
        SET not_before = GREATEST(public.blob_gc_queue.not_before, EXCLUDED.not_before);
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER embedded_subtitle_tracks_queue_deleted
    AFTER DELETE ON public.embedded_subtitle_tracks
    REFERENCING OLD TABLE AS old_rows
    FOR EACH STATEMENT
    EXECUTE FUNCTION public.queue_removed_embedded_subtitle_tracks();

CREATE TRIGGER embedded_subtitle_tracks_queue_replaced
    AFTER UPDATE ON public.embedded_subtitle_tracks
    REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
    FOR EACH STATEMENT
    EXECUTE FUNCTION public.queue_removed_embedded_subtitle_tracks();

-- A rescan that finds the file changed retires what was read from the old
-- version.
-- +goose StatementBegin
CREATE FUNCTION public.forget_changed_file_subtitle_tracks()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    DELETE FROM public.embedded_subtitle_tracks WHERE media_file_id = NEW.id;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER media_files_forget_changed_subtitle_tracks
    AFTER UPDATE OF file_size, file_modified_at ON public.media_files
    FOR EACH ROW
    WHEN (OLD.file_size IS DISTINCT FROM NEW.file_size OR OLD.file_modified_at IS DISTINCT FROM NEW.file_modified_at)
    EXECUTE FUNCTION public.forget_changed_file_subtitle_tracks();

-- +goose Down
DROP TRIGGER IF EXISTS media_files_forget_changed_subtitle_tracks ON public.media_files;
DROP FUNCTION IF EXISTS public.forget_changed_file_subtitle_tracks();
DROP TABLE IF EXISTS public.embedded_subtitle_track_claims;
DROP TABLE IF EXISTS public.embedded_subtitle_tracks;
DROP FUNCTION IF EXISTS public.queue_removed_embedded_subtitle_tracks();
-- The kept objects stay in storage: nothing names them after the rollback.
DELETE FROM public.blob_gc_queue WHERE prefix LIKE 'subtitles/embedded/%';
ALTER TABLE public.blob_gc_queue DROP CONSTRAINT blob_gc_queue_prefix_check;
ALTER TABLE public.blob_gc_queue ADD CONSTRAINT blob_gc_queue_prefix_check CHECK (
    prefix ~ '^chapter-images/[1-9][0-9]*/$'
    OR prefix ~ '^chapter-images/[1-9][0-9]*/((0|[1-9][0-9]*)(-[a-f0-9]{64})?|marker-[a-f0-9]{64}-[a-f0-9]{64})/w[1-9][0-9]*\.webp$'
    OR prefix ~ '^trickplay/[1-9][0-9]*/[1-9][0-9]*/$');
